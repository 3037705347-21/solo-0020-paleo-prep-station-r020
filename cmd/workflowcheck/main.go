package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID              string `json:"id"`
	SpecimenID      string `json:"specimen_id"`
	Status          string `json:"status"`
	CurrentRevision int    `json:"current_revision"`
	Method          string `json:"method"`
	Adhesive        string `json:"adhesive"`
	TargetExposure  int    `json:"target_exposure"`
}

type revisionView struct {
	Revision       int        `json:"revision"`
	Method         string     `json:"method"`
	Adhesive       string     `json:"adhesive"`
	TargetExposure int        `json:"target_exposure"`
	Reason         string     `json:"reason"`
	ChangedBy      string     `json:"changed_by"`
	FromSequence   int        `json:"from_sequence"`
	ToSequence     *int       `json:"to_sequence"`
	EffectiveFrom  *time.Time `json:"effective_from"`
	EffectiveTo    *time.Time `json:"effective_to"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Sequence int `json:"sequence"`
		Percent  int `json:"percent"`
		Revision int `json:"revision"`
	} `json:"progress"`
	Revisions []revisionView `json:"revisions"`
}

type errorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil

	case "revise-active-plan":
		return checkReviseActivePlan(client, server.URL)
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

// checkReviseActivePlan 覆盖活动方案变更的全部确定性行为：
// 生效范围固定、历史进度绑定版本且不可改写、重复提交/无效回退/合法回退、
// 乐观并发与真实并发竞争、以及完成后不能再变更。
func checkReviseActivePlan(client *http.Client, baseURL string) error {
	specimen, plan, err := completedPlanSetup(client, baseURL)
	if err != nil {
		return err
	}
	planID := plan.ID

	// 方案刚审批通过、尚未出现第一条进度时还不是 active，变更必须被拒绝。
	target85Early := 85
	if err := expectError(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure": target85Early,
			"reason":          "制备尚未开始，不能提前变更",
			"changed_by":      "preparator-chen",
		},
		http.StatusConflict,
		"plan_not_revisable",
	); err != nil {
		return err
	}

	// 第一条进度使方案进入 active，此时依据第 1 版条件。
	if err := recordProgress(client, baseURL, planID, 30, "矩阵剥离"); err != nil {
		return err
	}

	var rev2 revisionView
	target85 := 85
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure": target85,
			"adhesive":        "稀释 B72",
			"reason":          "现场发现关节面更深，需要提高暴露比例并更换黏结剂",
			"changed_by":      "preparator-chen",
		},
		http.StatusCreated,
		&rev2,
	); err != nil {
		return err
	}
	if rev2.Revision != 2 || rev2.TargetExposure != 85 || rev2.Adhesive != "稀释 B72" {
		return errors.New("revision 2 did not carry the submitted conditions")
	}
	if rev2.FromSequence != 2 || rev2.ToSequence != nil || rev2.EffectiveFrom == nil || rev2.EffectiveTo != nil {
		return errors.New("revision 2 effective range is not open-ended from sequence 2")
	}

	// 新进度必须依据当前有效版本 2。
	if err := recordProgress(client, baseURL, planID, 60, "加固暴露面"); err != nil {
		return err
	}

	// 重复提交：与当前版本条件完全相同（省略所有条件字段）→ 409。
	if err := expectError(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"reason":     "重复提交相同条件",
			"changed_by": "preparator-chen",
		},
		http.StatusConflict,
		"revision_unchanged",
	); err != nil {
		return err
	}

	// 回退到无效条件：目标暴露比例越界 → 400，且不产生新版本。
	badExposure := 120
	if err := expectError(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure": badExposure,
			"reason":          "尝试回退到越界条件",
			"changed_by":      "preparator-chen",
		},
		http.StatusBadRequest,
		"invalid_target_exposure",
	); err != nil {
		return err
	}

	// 合法回退到第 1 版的旧条件（80/可逆树脂）仍作为新版本 3 追加。
	target80 := 80
	var rev3 revisionView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure": target80,
			"adhesive":        "可逆树脂",
			"reason":          "深层骨面状况良好，恢复原定条件",
			"changed_by":      "curator-lin",
		},
		http.StatusCreated,
		&rev3,
	); err != nil {
		return err
	}
	if rev3.Revision != 3 || rev3.TargetExposure != 80 || rev3.FromSequence != 3 {
		return errors.New("rollback to a previously valid condition must append revision 3")
	}

	// 第 3 版也要实际承载一条新进度，保证每个历史版本都有非空有效范围。
	if err := recordProgress(client, baseURL, planID, 75, "沿旧条件精修"); err != nil {
		return err
	}

	// 乐观并发：拿着过期的 expected_revision=2 提交 → 409 revision_conflict。
	target70 := 70
	if err := expectError(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure":   target70,
			"reason":            "基于过期版本的并发提交",
			"changed_by":        "preparator-wang",
			"expected_revision": 2,
		},
		http.StatusConflict,
		"revision_conflict",
	); err != nil {
		return err
	}

	// 真实并发竞争：两笔都基于当前版本 3 且不带 expected_revision，仓储互斥
	// 把它们串行化：必须恰好一个成功（201），另一个以 revision_unchanged 或
	// revision_conflict 之外的确定方式失败——这里两笔提交的内容相同，后到者
	// 看到条件已被先到者改完，必然 409 revision_unchanged。
	target90 := 90
	body := map[string]any{
		"target_exposure": target90,
		"reason":          "并发竞争：同一条件同时提交",
		"changed_by":      "preparator-wang",
	}
	statuses, codes, err := postConcurrently(client, baseURL+"/plans/"+planID+"/revisions", body, 2)
	if err != nil {
		return err
	}
	created, conflicted := 0, 0
	for i, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			if codes[i] != "revision_unchanged" {
				return fmt.Errorf("losing concurrent revision got code %s", codes[i])
			}
			conflicted++
		default:
			return fmt.Errorf("concurrent revision returned unexpected status %d", status)
		}
	}
	if created != 1 || conflicted != 1 {
		return fmt.Errorf("concurrent revisions need exactly one success, got %d created and %d conflict", created, conflicted)
	}

	var snapshot planSnapshot
	if err := requestJSON(client, http.MethodGet, baseURL+"/plans/"+planID, nil, http.StatusOK, &snapshot); err != nil {
		return err
	}
	// 四版：1（基线）、2、3、4（并发先到者）。当前版本为 4，方案条件已同步。
	if len(snapshot.Revisions) != 4 {
		return fmt.Errorf("expected four revisions, got %d", len(snapshot.Revisions))
	}
	if snapshot.Plan.Status != "active" || snapshot.Plan.CurrentRevision != 4 || snapshot.Plan.TargetExposure != 90 {
		return fmt.Errorf("plan is not reflecting current revision 4: %+v", snapshot.Plan)
	}
	// 历史版本的有效范围必须已经被固定：第 1 版 [1,1]，第 2 版 [2,2]，
	// 第 3 版 [3,3]，第 4 版从 4 开始开放。
	wantRanges := [][2]int{{1, 1}, {2, 2}, {3, 3}}
	for i, want := range wantRanges {
		r := snapshot.Revisions[i]
		if r.FromSequence != want[0] || r.ToSequence == nil || *r.ToSequence != want[1] {
			return fmt.Errorf("revision %d range %d..%v, want %d..%d", r.Revision, r.FromSequence, r.ToSequence, want[0], want[1])
		}
		if r.EffectiveFrom == nil || r.EffectiveTo == nil || r.EffectiveTo.Before(*r.EffectiveFrom) {
			return fmt.Errorf("revision %d effective time range is not fixed", r.Revision)
		}
	}
	last := snapshot.Revisions[3]
	if last.Revision != 4 || last.ToSequence != nil || last.EffectiveTo != nil {
		return errors.New("current revision must keep an open effective range")
	}
	// 历史进度不可改写：三条进度分别绑定写入时的版本 1、2、3。
	if len(snapshot.Progress) != 3 {
		return fmt.Errorf("expected three progress entries, got %d", len(snapshot.Progress))
	}
	for i, wantRevision := range []int{1, 2, 3} {
		if snapshot.Progress[i].Revision != wantRevision || snapshot.Progress[i].Sequence != i+1 {
			return fmt.Errorf("progress %d bound to revision %d, want %d", i+1, snapshot.Progress[i].Revision, wantRevision)
		}
	}

	// 完成方案（100% 的新进度依据当前版本 4），此后任何变更都必须被拒绝。
	if err := recordProgress(client, baseURL, planID, 100, "终态加固"); err != nil {
		return err
	}
	if err := expectError(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/revisions",
		map[string]any{
			"target_exposure": target80,
			"reason":          "完成后尝试变更",
			"changed_by":      "preparator-chen",
		},
		http.StatusConflict,
		"plan_not_revisable",
	); err != nil {
		return err
	}
	// 完成后版本历史冻结：仍然只有四版，且快照中的方案已完成。
	if err := requestJSON(client, http.MethodGet, baseURL+"/plans/"+planID, nil, http.StatusOK, &snapshot); err != nil {
		return err
	}
	if snapshot.Plan.Status != "completed" || len(snapshot.Revisions) != 4 {
		return fmt.Errorf("completed plan must keep frozen revision history, got status %s and %d revisions", snapshot.Plan.Status, len(snapshot.Revisions))
	}
	// 完成后当前版本的有效范围也必须闭合到最后一条进度，全部版本区间冻结。
	final := snapshot.Revisions[3]
	if final.Revision != 4 || final.ToSequence == nil || *final.ToSequence != 4 || final.EffectiveTo == nil {
		return errors.New("final revision range must be closed at completion")
	}
	if len(snapshot.Progress) != 4 || snapshot.Progress[3].Revision != 4 {
		return errors.New("final progress must be bound to revision 4 and history must stay intact")
	}

	_ = specimen
	return nil
}

// expectError 提交请求并断言状态码与稳定错误码。
func expectError(client *http.Client, method, url string, body any, wantStatus int, wantCode string) error {
	status, raw, err := doRequest(client, method, url, body)
	if err != nil {
		return err
	}
	if status != wantStatus {
		return fmt.Errorf("request %s returned %d, want %d: %s", url, status, wantStatus, string(raw))
	}
	var decoded errorView
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("decode error body for %s: %w", url, err)
	}
	if decoded.Code != wantCode {
		return fmt.Errorf("request %s returned code %s, want %s", url, decoded.Code, wantCode)
	}
	return nil
}

// postConcurrently 以相同请求体发起 n 个并发 POST，返回每个响应的状态码与
// 错误码（成功响应的错误码为空字符串）。
func postConcurrently(client *http.Client, url string, body map[string]any, n int) ([]int, []string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	type result struct {
		status int
		code   string
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			entry := result{status: response.StatusCode}
			if response.StatusCode != http.StatusCreated {
				var decoded errorView
				if json.Unmarshal(raw, &decoded) == nil {
					entry.code = decoded.Code
				}
			}
			results[i] = entry
		}(i)
	}
	wg.Wait()
	statuses := make([]int, n)
	codes := make([]string, n)
	for i, entry := range results {
		if entry.err != nil {
			return nil, nil, entry.err
		}
		statuses[i] = entry.status
		codes[i] = entry.code
	}
	return statuses, codes, nil
}

func doRequest(client *http.Client, method, url string, body any) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, raw, nil
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: "PPS-2026-001",
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}

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
}

type revisionView struct {
	Revision              int  `json:"revision"`
	EffectiveFromSequence int  `json:"effective_from_sequence"`
	EffectiveToSequence   *int `json:"effective_to_sequence"`
}

type progressView struct {
	Percent      int `json:"percent"`
	PlanRevision int `json:"plan_revision"`
}

type planSnapshot struct {
	Plan      planView       `json:"plan"`
	Revisions []revisionView `json:"revisions"`
	Progress  []progressView `json:"progress"`
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

	case "amend-active-plan":
		_, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "基质剥离"); err != nil {
			return err
		}
		amendment := contracts.AmendPlanRequest{
			Method:         "气动笔修整",
			Adhesive:       "可逆树脂",
			TargetExposure: 90,
			Reason:         "现场发现围岩破碎，需要降低扰动",
			ChangedBy:      "preparator-chen",
			BaseRevision:   1,
		}
		var first revisionView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/amendments",
			amendment, http.StatusCreated, &first,
		); err != nil {
			return err
		}
		if first.Revision != 2 || first.EffectiveFromSequence != 2 {
			return errors.New("amendment did not pin revision 2 from the next sequence")
		}
		// 重复提交返回同一版本，不新建
		var replay revisionView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/amendments",
			amendment, http.StatusOK, &replay,
		); err != nil {
			return err
		}
		if replay.Revision != first.Revision {
			return errors.New("duplicate amendment created a new revision")
		}
		// 过期基准版本冲突
		stale := amendment
		stale.Method = "超声波清洗"
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/amendments",
			stale, http.StatusConflict, nil,
		); err != nil {
			return err
		}
		// 目标回退到低于已记录进度的无效条件冲突
		regressed := amendment
		regressed.BaseRevision = 2
		regressed.TargetExposure = 20
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/amendments",
			regressed, http.StatusConflict, nil,
		); err != nil {
			return err
		}
		// 依据过期版本记录进度冲突
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/progress",
			contracts.ProgressRequest{
				Stage: "边缘清理", Percent: 60, Note: "本地链路检查",
				RecordedBy: "preparator-chen", PlanRevision: 1,
			},
			http.StatusConflict, nil,
		); err != nil {
			return err
		}
		// 依据当前有效版本记录进度
		var entry progressView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/progress",
			contracts.ProgressRequest{
				Stage: "边缘清理", Percent: 60, Note: "本地链路检查",
				RecordedBy: "preparator-chen", PlanRevision: 2,
			},
			http.StatusCreated, &entry,
		); err != nil {
			return err
		}
		if entry.PlanRevision != 2 {
			return errors.New("progress entry was not stamped with the current revision")
		}
		// 并发变更只有一个成功
		if err := raceAmendments(client, server.URL, plan.ID, 2); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.CurrentRevision != 3 || len(snapshot.Revisions) != 3 {
			return errors.New("snapshot does not expose exactly three revisions")
		}
		if snapshot.Revisions[0].EffectiveToSequence == nil || *snapshot.Revisions[0].EffectiveToSequence != 1 {
			return errors.New("initial revision window is not pinned to the first entry")
		}
		if snapshot.Revisions[1].EffectiveToSequence == nil || *snapshot.Revisions[1].EffectiveToSequence != 2 {
			return errors.New("second revision window is not pinned to the second entry")
		}
		if snapshot.Revisions[2].EffectiveToSequence != nil {
			return errors.New("current revision window must stay open before completion")
		}
		if snapshot.Progress[0].PlanRevision != 1 || snapshot.Progress[1].PlanRevision != 2 {
			return errors.New("historical progress entries were rewritten")
		}
		// 完成后不能再变更
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		afterComplete := amendment
		afterComplete.BaseRevision = 3
		afterComplete.TargetExposure = 95
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/amendments",
			afterComplete, http.StatusConflict, nil,
		); err != nil {
			return err
		}
		var closed planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &closed); err != nil {
			return err
		}
		if closed.Revisions[2].EffectiveToSequence == nil || *closed.Revisions[2].EffectiveToSequence != 3 {
			return errors.New("completed plan did not close the last revision window")
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
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
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

// raceAmendments 并发提交两个不同的变更，断言恰好一个成功、一个冲突。
func raceAmendments(client *http.Client, baseURL, planID string, baseRevision int) error {
	results := make(chan int, 2)
	for _, method := range []string{"显微针修", "化学软化"} {
		go func(name string) {
			status, err := postStatus(client, baseURL+"/plans/"+planID+"/amendments", contracts.AmendPlanRequest{
				Method:         name,
				Adhesive:       "可逆树脂",
				TargetExposure: 92,
				Reason:         "并发确定性验证",
				ChangedBy:      "preparator-chen",
				BaseRevision:   baseRevision,
			})
			if err != nil {
				status = -1
			}
			results <- status
		}(method)
	}
	created, conflict := 0, 0
	for i := 0; i < 2; i++ {
		switch <-results {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		default:
			return errors.New("concurrent amendment returned an unexpected status")
		}
	}
	if created != 1 || conflict != 1 {
		return fmt.Errorf("concurrent amendments must yield exactly one success, got %d success and %d conflict", created, conflict)
	}
	return nil
}

func postStatus(client *http.Client, url string, body any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	response, err := client.Post(url, "application/json", bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20)); err != nil {
		return 0, err
	}
	return response.StatusCode, nil
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

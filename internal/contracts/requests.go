package contracts

type RegisterSpecimenRequest struct {
	CatalogNumber string `json:"catalog_number"`
	FieldLabel    string `json:"field_label"`
	TaxonHint     string `json:"taxon_hint"`
	MatrixKind    string `json:"matrix_kind"`
	JacketNotes   string `json:"jacket_notes"`
}

type CreatePlanRequest struct {
	Objective      string `json:"objective"`
	Method         string `json:"method"`
	Adhesive       string `json:"adhesive"`
	TargetExposure int    `json:"target_exposure"`
}

type ApprovalRequest struct {
	ApprovedBy string `json:"approved_by"`
}

type ProgressRequest struct {
	Stage      string `json:"stage"`
	Percent    int    `json:"percent"`
	Note       string `json:"note"`
	RecordedBy string `json:"recorded_by"`
}

// RevisionRequest 是活动方案的一次现场条件变更。Method/Adhesive/
// TargetExposure 均为可选：未提交的字段沿用当前有效版本。Adhesive 使用
// 指针以支持显式清空黏结材料。
type RevisionRequest struct {
	Method         *string `json:"method,omitempty"`
	Adhesive       *string `json:"adhesive,omitempty"`
	TargetExposure *int    `json:"target_exposure,omitempty"`
	Reason         string  `json:"reason"`
	ChangedBy      string  `json:"changed_by"`
	// ExpectedRevision 为可选的乐观并发条件：给出时必须等于当前有效版本号。
	// 并发的第二笔变更会读到已递增的版本号，从而得到确定的 409 结果。
	ExpectedRevision *int `json:"expected_revision,omitempty"`
}

type ReleaseRequest struct {
	Purpose    string `json:"purpose"`
	Recipient  string `json:"recipient"`
	ApprovedBy string `json:"approved_by"`
}

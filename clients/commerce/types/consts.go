package types

type Interval string

const (
	Yearly  Interval = "year"
	Monthly Interval = "month"
)

type KYCStatus string

const (
	KYCStatusApproved  KYCStatus = "approved"
	KYCStatusDenied    KYCStatus = "denied"
	KYCStatusPending   KYCStatus = "pending"
	KYCStatusInitiated KYCStatus = "initiated"
)

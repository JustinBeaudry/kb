package domain

import "time"

type Policy struct {
	Revision                                                    string
	Paused                                                      bool
	ExcludedPaths, ExcludedFields, AllowedModelRoutes           []string
	RawHistoryRetention, InactiveTaskRetention, BackupRetention time.Duration
	SpoolLimitBytes                                             int64
}

// Usage deliberately distinguishes unknown settlement from measured zero.
// Unknown costs retain their reservation until a later reconciliation.
type Usage struct {
	Tokens           int64
	TokensKnown      bool
	CostMicroUSD     int64
	CostKnown        bool
	ReservedMicroUSD int64
}

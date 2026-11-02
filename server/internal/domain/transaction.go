package domain

// Tx identifies an active transaction without exposing a database driver.
// It must not escape its callback or be used concurrently.
type Tx interface{ TransactionID() string }

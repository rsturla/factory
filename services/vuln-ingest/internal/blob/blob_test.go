package blob

// Compile-time interface compliance checks.
var (
	_ Store = (*LocalStore)(nil)
	_ Store = (*S3Store)(nil)
)

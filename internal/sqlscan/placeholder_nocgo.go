//go:build !cgo

package sqlscan

// normalisePostgres without cgo leaves the submission alone.
//
// Its only consumer is ClassifyPostgres, which in this build has no grammar
// and refuses everything it is given, so there is nothing for a normalised
// form to be correct for. Hand-rolling a second implementation to feed a
// classifier that will refuse regardless would be two things to keep in step
// for no benefit, and keeping two walks in step is precisely what this package
// has already got wrong once.
func normalisePostgres(sql string, _ map[string]string) string { return sql }

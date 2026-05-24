package core

// QuotaProvider tells the engine the byte budget for each tenant. The
// engine snapshots Limit and Used at job-start time and stows them on the
// Job so modules can refuse writes that would push the tenant over.
//
// Production-grade enforcement should be paired with OS-level quotas
// (XFS project quotas, ZFS quotas, cgroups blkio) because the in-process
// check is a snapshot — concurrent file writes from the same tenant can
// briefly exceed the limit between Used() and the actual write.
type QuotaProvider interface {
	// Limit returns the byte budget for tenant. Zero means unlimited.
	Limit(tenant string) int64

	// Used returns the bytes currently held by tenant. Caller treats it
	// as a snapshot; staleness is acceptable given the documented race.
	Used(tenant string) (int64, error)
}

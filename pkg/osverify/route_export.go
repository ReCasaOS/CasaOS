package osverify

// DefaultRoute is the interface that the IPv4 default route of the text of /proc/net/route goes out by, and "" when there is none: what the facts say of the route, for the core
// that waits for a machine that has restarted to have its network back before it checks it.
func DefaultRoute(procNetRoute string) string { return defaultRoute(procNetRoute) }

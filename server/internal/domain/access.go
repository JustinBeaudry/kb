package domain

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// AccessOutsideGrant stands in for a project a client asked for but was never
// granted. Recording the requested ID itself would let any authenticated
// client write arbitrary strings, and unbounded rows, into the audit trail.
const AccessOutsideGrant = "*"

// accessKeySeparator never appears in an accepted identifier.
const accessKeySeparator = "\x1f"

// maxAccessPerRequest bounds one request's observations; a request touches at
// most a handful of projects and capabilities.
const maxAccessPerRequest = 256

// AccessRollup counts authorization decisions for one client, project,
// capability and UTC day. It is the durable, owner-readable access audit
// record: counts and times only, never request content, bearers or sources.
type AccessRollup struct {
	ClientID   string    `json:"client_id"`
	ProjectID  string    `json:"project_id"`
	Capability string    `json:"capability"`
	Day        string    `json:"day"`
	Allowed    int64     `json:"allowed"`
	Denied     int64     `json:"denied"`
	FirstAt    time.Time `json:"first_at"`
	LastAt     time.Time `json:"last_at"`
}

// Key orders rollups newest day first when sorted descending, and is the
// unique graph key and the pagination cursor.
func (a AccessRollup) Key() string {
	return strings.Join([]string{a.Day, a.ClientID, a.ProjectID, a.Capability}, accessKeySeparator)
}

// AccessQuery filters the owner's view of the audit trail. Empty client or
// project means any. Since is an inclusive UTC day (YYYY-MM-DD).
type AccessQuery struct {
	ClientID  string
	ProjectID string
	Since     string
	Limit     int
	After     string
}

// AccessPage is one page of rollups, newest day first.
type AccessPage struct {
	Rollups []AccessRollup `json:"rollups"`
	Next    string         `json:"next,omitempty"`
}

// AccessDay is the UTC calendar day an access is counted under.
func AccessDay(at time.Time) string { return at.UTC().Format(time.DateOnly) }

// ValidAccessDay reports whether s is a YYYY-MM-DD calendar day.
func ValidAccessDay(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil && len(s) == len(time.DateOnly)
}

// AccessObservation is one authorization decision inside one request.
type AccessObservation struct {
	ClientID   string
	ProjectID  string
	Capability string
	Allowed    bool
}

type accessObservationKey struct{ client, project, capability string }

// AccessCollector gathers one request's authorization decisions. Services
// often check the same access twice (before and inside a transaction), so a
// repeated decision counts once, and a denial wins over an earlier allow.
type AccessCollector struct {
	mu   sync.Mutex
	seen map[accessObservationKey]bool
}

func NewAccessCollector() *AccessCollector {
	return &AccessCollector{seen: map[accessObservationKey]bool{}}
}

func (c *AccessCollector) observe(p Principal, capability string, projects []string, allowed bool) {
	if c == nil || !auditableID(p.ClientID) || !auditableID(capability) {
		return
	}
	if len(projects) == 0 {
		projects = []string{""}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, project := range projects {
		if project != "" && (!auditableID(project) || !slices.Contains(p.ProjectIDs, project)) {
			project = AccessOutsideGrant
		}
		key := accessObservationKey{p.ClientID, project, capability}
		prior, seen := c.seen[key]
		if !seen && len(c.seen) >= maxAccessPerRequest {
			continue
		}
		c.seen[key] = (!seen || prior) && allowed
	}
}

// Observations returns the request's decisions in a stable order.
func (c *AccessCollector) Observations() []AccessObservation {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AccessObservation, 0, len(c.seen))
	for k, allowed := range c.seen {
		out = append(out, AccessObservation{ClientID: k.client, ProjectID: k.project, Capability: k.capability, Allowed: allowed})
	}
	slices.SortFunc(out, func(a, b AccessObservation) int {
		return strings.Compare(a.ClientID+accessKeySeparator+a.ProjectID+accessKeySeparator+a.Capability, b.ClientID+accessKeySeparator+b.ProjectID+accessKeySeparator+b.Capability)
	})
	return out
}

// ValidAccessID reports whether s can name a client, project or capability in
// the audit trail, as a stored value or a filter.
func ValidAccessID(s string) bool { return auditableID(s) }

func auditableID(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, accessKeySeparator+"\x00\r\n")
}

type accessCollectorKey struct{}

// WithAccessCollector attaches a request's collector. Contexts without one,
// such as host administration and the worker, record nothing.
func WithAccessCollector(ctx context.Context, c *AccessCollector) context.Context {
	return context.WithValue(ctx, accessCollectorKey{}, c)
}

// ObserveAccess records one authorization decision on the request's collector.
func ObserveAccess(ctx context.Context, p Principal, capability string, projects []string, allowed bool) {
	c, _ := ctx.Value(accessCollectorKey{}).(*AccessCollector)
	c.observe(p, capability, projects, allowed)
}

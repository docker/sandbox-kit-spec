package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// Surface is the permission-relevant projection of a descriptor: everything
// the host must grant, normalized (sorted, deduplicated) so two surfaces
// compare structurally. The gate stores a kit's surface in the lock and
// diffs it against a candidate update's; version movement that stays within
// the granted surface applies silently, movement that widens it stops for
// approval.
type Surface struct {
	NetworkInstallAllow []string `json:"networkInstallAllow,omitempty"`
	NetworkInstallDeny  []string `json:"networkInstallDeny,omitempty"`
	NetworkRuntimeAllow []string `json:"networkRuntimeAllow,omitempty"`
	NetworkRuntimeDeny  []string `json:"networkRuntimeDeny,omitempty"`

	// The HTTP lists are what may be done over the allowed connections,
	// one atomic entry per method, host, and path as "<METHOD>
	// <host><path>". A host with no allow rule bounding it appears under
	// every method, because that is what it grants. Kept apart from the
	// host lists because the two grant differently: a host list entry
	// opens a connection, while these bound what may be done over it, so
	// a gate naming both "network" could not say whether an update opened
	// a new host or widened an existing one.
	NetworkInstallHTTPAllow []string `json:"networkInstallHTTPAllow,omitempty"`
	NetworkInstallHTTPDeny  []string `json:"networkInstallHTTPDeny,omitempty"`
	NetworkRuntimeHTTPAllow []string `json:"networkRuntimeHTTPAllow,omitempty"`
	NetworkRuntimeHTTPDeny  []string `json:"networkRuntimeHTTPDeny,omitempty"`

	CredentialsInstall []string `json:"credentialsInstall,omitempty"`
	CredentialsRuntime []string `json:"credentialsRuntime,omitempty"`

	// SSHAgent holds what the SSH agent may sign, per phase: "<phase>"
	// for an unbounded grant, "<phase> sign <namespace>" and "<phase>
	// authenticate <[user@]host>" for a bounded one. Kept apart from
	// credentials because the agent grants more than one service: an
	// unbounded grant signs with every key it holds, for anything the
	// sandbox asks.
	SSHAgent []string `json:"sshAgent,omitempty"`

	StoragePaths []string `json:"storagePaths,omitempty"`

	// SkillsPaths are the in-container paths the host's shared skills
	// store is asked for. Kept apart from StoragePaths because the two
	// grant different things: a volume is storage the sandbox is given,
	// while this is a host directory the user filled with `sbx skills
	// import` being handed to a kit. A gate naming both "storage" would
	// not say which one was gained.
	SkillsPaths []string `json:"skillsPaths,omitempty"`

	// SkillsWritePaths are the subset of SkillsPaths the kit asks to
	// write. Separate from the paths themselves so access cannot be
	// confused with a path: write is the larger grant, and a sandbox that
	// can rewrite the shared store affects every later one.
	SkillsWritePaths []string `json:"skillsWritePaths,omitempty"`
	Ports            []string `json:"ports,omitempty"`
	USB              []string `json:"usb,omitempty"`
	Privileged       bool     `json:"privileged,omitempty"`

	// Services holds one entry per requested runtime-provided service:
	// the type string, with a digest of the config when one is present,
	// so a config change moves the surface the way any widening does.
	// Optional requests are surfaced too — optional only changes what
	// happens when the host cannot provide the service, not what is
	// granted when it can.
	Services []string `json:"services,omitempty"`
}

// SurfaceOf projects a descriptor's needs onto its permission surface.
// Supply the selected, merged descriptor for enforcement. On an unselected
// declaration this conservatively inventories all members, without group grants.
// Well-known types decode into the direction-aware fields the gate diffs
// with per-category semantics; every other type — and any known entry
// whose config fails to decode — lands in Services as type + config
// digest, so nothing a kit requests can escape the surface.
func SurfaceOf(d *Descriptor) Surface {
	var s Surface
	for _, n := range DeclaredCapabilities(d.Capabilities) {
		switch n.Type {
		case CapabilityNetworkPolicy, CapabilityNetworkPolicyV2:
			p, err := NetworkPolicyV2Of([]Capability{n})
			if err != nil || p == nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			if r := p.Install; r != nil {
				s.NetworkInstallAllow = append(s.NetworkInstallAllow, connectionHosts(r.Allow)...)
				s.NetworkInstallDeny = append(s.NetworkInstallDeny, connectionHosts(r.Deny)...)
				s.NetworkInstallHTTPAllow = append(s.NetworkInstallHTTPAllow, entrySurface(r.Allow)...)
				s.NetworkInstallHTTPDeny = append(s.NetworkInstallHTTPDeny, entrySurface(r.Deny)...)
			}
			if r := p.Runtime; r != nil {
				s.NetworkRuntimeAllow = append(s.NetworkRuntimeAllow, connectionHosts(r.Allow)...)
				s.NetworkRuntimeDeny = append(s.NetworkRuntimeDeny, connectionHosts(r.Deny)...)
				s.NetworkRuntimeHTTPAllow = append(s.NetworkRuntimeHTTPAllow, entrySurface(r.Allow)...)
				s.NetworkRuntimeHTTPDeny = append(s.NetworkRuntimeHTTPDeny, entrySurface(r.Deny)...)
			}
		case CapabilityCredential:
			var c Credential
			if err := DecodeCapabilityConfig(n, &c); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			for _, phase := range c.Phase {
				if phase == "install" {
					s.CredentialsInstall = append(s.CredentialsInstall, c.Service)
				} else {
					s.CredentialsRuntime = append(s.CredentialsRuntime, c.Service)
				}
			}
		case CapabilitySSHAgent:
			var a SSHAgent
			if err := DecodeCapabilityConfig(n, &a); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			s.SSHAgent = append(s.SSHAgent, sshAgentSurface(a)...)
		case CapabilityAgentSkills:
			var sk AgentSkills
			if err := DecodeCapabilityConfig(n, &sk); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			s.SkillsPaths = append(s.SkillsPaths, sk.Path)
			if SkillsWritable(sk) {
				s.SkillsWritePaths = append(s.SkillsWritePaths, sk.Path)
			}
		case CapabilityVolume:
			var v Volume
			if err := DecodeCapabilityConfig(n, &v); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			s.StoragePaths = append(s.StoragePaths, v.Path)
		case CapabilityPort:
			var p Port
			if err := DecodeCapabilityConfig(n, &p); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			s.Ports = append(s.Ports, PortKey(p))
		case CapabilityUSBDevice:
			var u USBDevice
			if err := DecodeCapabilityConfig(n, &u); err != nil {
				s.Services = append(s.Services, capabilitySurfaceEntry(n))
				continue
			}
			if u.Class != "" {
				s.USB = append(s.USB, "class:"+u.Class)
			} else {
				s.USB = append(s.USB, u.VendorID+":"+u.ProductID)
			}
		case CapabilityPrivileged:
			s.Privileged = true
		case CapabilityResources:
			// Resource limits constrain the kit rather than grant it
			// anything; they are not permission surface.
		case CapabilityLongRunning:
			// Session-independent lifetime grants no access across the boundary.
		case CapabilityAgentSkill:
			// Bundled instructions expose only content already carried by the image.
		case CapabilityAgentSessions, CapabilityAgentInteractiveSessions, CapabilityLifecycle, CapabilityAgentContext, CapabilitySbx:
			// Content-trust declarations, not grants: session verbs,
			// lifecycle hooks, and files run inside the sandbox on the
			// entrypoint's trust plane, and agent context is instruction
			// text the agent reads. Nothing crosses the boundary the
			// gate guards by declaring them. sbx@1 is the same: it asks
			// the host to launch the agent the platform's way and to
			// honor the identity the image already states, which grants
			// access to nothing.
		default:
			s.Services = append(s.Services, capabilitySurfaceEntry(n))
		}
	}
	s.NetworkInstallAllow = normalized(s.NetworkInstallAllow)
	s.NetworkInstallDeny = normalized(s.NetworkInstallDeny)
	s.NetworkInstallHTTPAllow = normalized(s.NetworkInstallHTTPAllow)
	s.NetworkInstallHTTPDeny = normalized(s.NetworkInstallHTTPDeny)
	s.NetworkRuntimeAllow = normalized(s.NetworkRuntimeAllow)
	s.NetworkRuntimeDeny = normalized(s.NetworkRuntimeDeny)
	s.NetworkRuntimeHTTPAllow = normalized(s.NetworkRuntimeHTTPAllow)
	s.NetworkRuntimeHTTPDeny = normalized(s.NetworkRuntimeHTTPDeny)
	s.CredentialsInstall = normalized(s.CredentialsInstall)
	s.CredentialsRuntime = normalized(s.CredentialsRuntime)
	s.SSHAgent = normalized(s.SSHAgent)
	s.StoragePaths = normalized(s.StoragePaths)
	s.SkillsPaths = normalized(s.SkillsPaths)
	s.SkillsWritePaths = normalized(s.SkillsWritePaths)
	s.Ports = normalized(s.Ports)
	s.USB = normalized(s.USB)
	s.Services = normalized(s.Services)
	return s
}

// connectionHosts is the hosts of the unbounded entries: what the phase
// may reach at the connection level, for any protocol. A bounded entry
// grants matching HTTP requests and nothing else, so its hosts are not
// here — they appear only in the HTTP projection below.
func connectionHosts(entries []NetworkEntry) []string {
	var out []string
	for _, e := range entries {
		if !e.Bounded() {
			out = append(out, e.Hosts...)
		}
	}
	return normalized(out)
}

// entrySurface renders entries as atomic grants: one per method, host,
// and path, so the gate compares them as sets and a narrowing (dropping a
// method, adding a deny) does not read as a new grant the way an entry
// rendered whole would. An unbounded entry is every method on every path,
// which is what it grants.
//
// MethodAny stays the wildcard it is: it admits extension methods no
// finite enumeration names (PROPFIND, MKCOL), so expanding it to the
// known list would make ANY and the enumeration one surface and let a
// locked explicit list widen to ANY without gating.
func entrySurface(entries []NetworkEntry) []string {
	var out []string
	for _, e := range entries {
		methods := EntryMethods(e)
		if slices.Contains(methods, MethodAny) {
			methods = []string{MethodAny}
		}
		for _, host := range e.Hosts {
			for _, m := range methods {
				for _, p := range EntryPaths(e) {
					out = append(out, m+" "+host+p)
				}
			}
		}
	}
	return out
}

// capabilitySurfaceEntry renders one typed request for the surface: the type
// alone when there is no config, the type plus a canonical config digest
// when there is — equal configs compare equal, any change moves the
// entry.
func capabilitySurfaceEntry(n Capability) string {
	if len(n.Config) == 0 {
		return n.Type
	}
	canonical, err := json.Marshal(n.Config)
	if err != nil {
		return n.Type + "+unencodable-config"
	}
	sum := sha256.Sum256(canonical)
	return n.Type + "+" + hex.EncodeToString(sum[:6])
}

func normalized(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Widening is one way a candidate surface asks for more than a granted one.
type Widening struct {
	// Category names the permission class, e.g. "network.runtime.allow".
	Category string `json:"category"`
	// Detail is the specific grant being requested, human-readable.
	Detail string `json:"detail"`
}

func (w Widening) String() string {
	return w.Category + ": " + w.Detail
}

// DiffWidenings reports every way candidate exceeds granted. Removals and
// narrowings are not reported: giving up a permission needs no approval.
// A removed deny entry IS a widening — the deny was part of what made the
// grant acceptable.
func DiffWidenings(granted, candidate Surface) []Widening {
	var out []Widening
	add := func(category string, details []string) {
		for _, d := range details {
			out = append(out, Widening{Category: category, Detail: d})
		}
	}

	add("network.install.allow", missingFrom(granted.NetworkInstallAllow, candidate.NetworkInstallAllow))
	add("network.runtime.allow", missingFrom(granted.NetworkRuntimeAllow, candidate.NetworkRuntimeAllow))
	add("network.install.deny-removed", missingFrom(candidate.NetworkInstallDeny, granted.NetworkInstallDeny))
	add("network.runtime.deny-removed", missingFrom(candidate.NetworkRuntimeDeny, granted.NetworkRuntimeDeny))
	add("network.install.http.allow", httpWidenings(
		granted.NetworkInstallAllow, candidate.NetworkInstallAllow,
		completeHTTPGrants(granted.NetworkInstallAllow, granted.NetworkInstallHTTPAllow),
		candidate.NetworkInstallHTTPAllow))
	add("network.runtime.http.allow", httpWidenings(
		granted.NetworkRuntimeAllow, candidate.NetworkRuntimeAllow,
		completeHTTPGrants(granted.NetworkRuntimeAllow, granted.NetworkRuntimeHTTPAllow),
		candidate.NetworkRuntimeHTTPAllow))
	add("network.install.http.deny-removed", httpDenyRemovals(
		granted.NetworkInstallDeny, candidate.NetworkInstallDeny,
		granted.NetworkInstallHTTPDeny, candidate.NetworkInstallHTTPDeny))
	add("network.runtime.http.deny-removed", httpDenyRemovals(
		granted.NetworkRuntimeDeny, candidate.NetworkRuntimeDeny,
		granted.NetworkRuntimeHTTPDeny, candidate.NetworkRuntimeHTTPDeny))
	add("credentials.install", missingFrom(granted.CredentialsInstall, candidate.CredentialsInstall))
	add("credentials.runtime", missingFrom(granted.CredentialsRuntime, candidate.CredentialsRuntime))
	add("ssh-agent", sshAgentWidenings(granted.SSHAgent, candidate.SSHAgent))
	add("storage", missingFrom(granted.StoragePaths, candidate.StoragePaths))
	add("skills", missingFrom(granted.SkillsPaths, candidate.SkillsPaths))
	add("skills.write", missingFrom(granted.SkillsWritePaths, candidate.SkillsWritePaths))
	add("ports", missingFrom(granted.Ports, candidate.Ports))
	add("usb", missingFrom(granted.USB, candidate.USB))
	if !granted.Privileged && candidate.Privileged {
		out = append(out, Widening{Category: "privileged", Detail: "requests privileged execution"})
	}
	add("services", missingFrom(granted.Services, candidate.Services))
	return out
}

// httpWidenings reports the HTTP grants candidate adds over granted, less
// those a newly allowed host already accounts for. Allowing a host is one
// grant, and the host list has just reported it; repeating it once per
// method would bury the case this category exists for — an already
// granted host losing the rules that bounded it. Hosts compare with the
// same port normalization validation uses, or "host:443" beside a rule
// naming "host" would report the host and every method both.
func httpWidenings(grantedHosts, candidateHosts, grantedHTTP, candidateHTTP []string) []string {
	fresh := map[string]bool{}
	for _, h := range missingFrom(grantedHosts, candidateHosts) {
		fresh[h] = true
	}
	// suppressed reports whether an HTTP entry's host is accounted for by
	// the freshly allowed hosts: the host list just reported those, and
	// an entry BOUNDING a new host is not an extra grant. Ports matter —
	// adding :8443 must not swallow a widening on the already-granted
	// :443 — so a port-qualified entry needs its own port fresh, and an
	// unqualified entry (which reaches only allowed ports) needs every
	// allowed port of its name fresh.
	suppressed := func(entryHost string) bool {
		name, port, qualified := strings.Cut(entryHost, ":")
		if qualified {
			_ = port
			return fresh[entryHost] || fresh[name]
		}
		any := false
		for _, h := range candidateHosts {
			if stripPort(h) != name {
				continue
			}
			any = true
			if !fresh[h] {
				return false
			}
		}
		return any
	}
	// Coverage is containment, not string equality: a granted ANY covers
	// every method on its host and path, and a granted /prefix/** covers
	// every narrower path under it — otherwise narrowing a path would
	// read as a widening because the strings differ. Nothing but ANY
	// covers a candidate's ANY, and only exact or /**-prefix containment
	// covers a path; anything subtler reports as a widening, which errs
	// toward gating.
	granted := make([]httpEntry, 0, len(grantedHTTP))
	for _, e := range grantedHTTP {
		granted = append(granted, parseHTTPEntry(e))
	}
	var out []string
	for _, e := range candidateHTTP {
		c := parseHTTPEntry(e)
		covered := false
		for _, g := range granted {
			if !hostCovers(g.host, c.host) {
				continue
			}
			if g.method != c.method && g.method != MethodAny {
				continue
			}
			if pathPatternCovers(g.path, c.path) {
				covered = true
				break
			}
		}
		if !covered && !suppressed(c.host) {
			out = append(out, e)
		}
	}
	return out
}

// hostCovers reports whether a granting host spelling covers a candidate
// one. host:port is a single-port grant: an unqualified host covers every
// port, a port-qualified one covers exactly its port — collapsing them
// would let POST granted on :8443 cover a new POST grant on :443.
//
// The universal patterns cover every host, which is the one pattern
// relation this package decides. Since a bare entry may be a pattern
// beside a bounded one, a policy granting "**" and then bounding a host
// inside it adds nothing, and without this the gate would prompt for a
// narrowing. Narrower globs are deliberately not expanded, here as in
// validateInjectWithinAllow: deciding whether "*.example.com" covers
// "api.example.com" is the runtime's matcher, and guessing at it would
// either under-report a widening or bind the gate to a matcher it does
// not own. They fail safe instead — an uncovered candidate reads as a
// widening, so the gate over-prompts rather than granting silently.
func hostCovers(granting, candidate string) bool {
	if granting == "*" || granting == "**" {
		return true
	}
	gName, gPort, _ := strings.Cut(granting, ":")
	cName, cPort, _ := strings.Cut(candidate, ":")
	return gName == cName && (gPort == "" || gPort == cPort)
}

// httpDenyRemovals reports the granted denies the candidate no longer
// makes, less those a dropped connection deny already accounts for.
// Coverage is containment with the roles reversed from allows: a
// candidate deny of ANY /private/** still denies what GET /private/**
// denied, so strengthening a deny never reads as a permission widening.
//
// A bare deny projects twice — into the host list and as ANY host/** —
// because an unbounded entry refuses every request as well as the
// connection. Dropping it is one loss, and the host list reports it, so
// repeating it here would say the same thing twice. Bounded denies have
// no host-list counterpart and are reported as they always were.
func httpDenyRemovals(grantedHosts, candidateHosts, grantedDeny, candidateDeny []string) []string {
	dropped := map[string]bool{}
	for _, h := range missingFrom(candidateHosts, grantedHosts) {
		dropped[h] = true
	}
	candidates := make([]httpEntry, 0, len(candidateDeny))
	for _, e := range candidateDeny {
		candidates = append(candidates, parseHTTPEntry(e))
	}
	var out []string
	for _, e := range grantedDeny {
		g := parseHTTPEntry(e)
		if dropped[g.host] {
			continue
		}
		covered := false
		for _, c := range candidates {
			if !hostCovers(c.host, g.host) {
				continue
			}
			if c.method != g.method && c.method != MethodAny {
				continue
			}
			if pathPatternCovers(c.path, g.path) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, e)
		}
	}
	return out
}

// httpEntry is one parsed "<METHOD> <host><path>" surface entry. The host
// keeps its port: host:port is a single-port grant.
type httpEntry struct {
	method, host, path string
}

func parseHTTPEntry(entry string) httpEntry {
	method, rest, _ := strings.Cut(entry, " ")
	host, pathPart, ok := strings.Cut(rest, "/")
	if !ok {
		return httpEntry{method: method, host: host, path: "/"}
	}
	return httpEntry{method: method, host: host, path: "/" + pathPart}
}

// pathPatternCovers reports whether the granted pattern contains the
// candidate one: equal, or granted ends in /** and the candidate lives
// under that prefix.
func pathPatternCovers(granted, candidate string) bool {
	if granted == candidate {
		return true
	}
	if prefix, ok := strings.CutSuffix(granted, "**"); ok && strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(candidate, prefix) || candidate+"/" == prefix
	}
	return false
}

// completeHTTPGrants reconstructs the unbounded HTTP grants a granted
// surface implies for hosts no rule bounds. Surfaces serialized before
// the HTTP fields existed carry hosts and nothing else, yet an allowed
// host granted all of HTTP under that model; diffing such a lock against
// a freshly computed surface without this reconstruction would report one
// false widening per method for an unchanged kit. For surfaces SurfaceOf
// produced, the expansion already happened and this adds nothing.
func completeHTTPGrants(hosts, grants []string) []string {
	// Bounded at the NAME level, matching entrySurface: an entry naming
	// any port of a host bounds the host, so its other ports carry no
	// implicit grant to reconstruct.
	bounded := map[string]bool{}
	for _, entry := range grants {
		bounded[stripPort(httpEntryHost(entry))] = true
	}
	var entries []NetworkEntry
	for _, h := range hosts {
		if !bounded[stripPort(h)] {
			entries = append(entries, NetworkEntry{Hosts: []string{h}})
		}
	}
	if len(entries) == 0 {
		return grants
	}
	return normalized(append(append([]string{}, grants...), entrySurface(entries)...))
}

// httpEntryHost reads the host out of a "<METHOD> <host><path>" entry.
// The method holds no space and the path opens the first "/", neither of
// which a domain pattern contains, so the two cuts are exact.
func httpEntryHost(entry string) string {
	_, rest, ok := strings.Cut(entry, " ")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	return host
}

// missingFrom returns the entries of candidate that granted does not contain.
func missingFrom(granted, candidate []string) []string {
	have := map[string]bool{}
	for _, g := range granted {
		have[g] = true
	}
	var out []string
	for _, c := range candidate {
		if !have[c] {
			out = append(out, c)
		}
	}
	return out
}

// FormatWidenings renders a widening list for an approval prompt.
func FormatWidenings(ws []Widening) string {
	lines := make([]string, len(ws))
	for i, w := range ws {
		lines[i] = "  + " + w.String()
	}
	return strings.Join(lines, "\n")
}

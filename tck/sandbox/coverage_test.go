package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// normative matches a requirement statement in the specification. The
// pages are written so that each line carries at most one, which is what
// lets a line-level anchor identify a statement.
var normative = regexp.MustCompile(`\b(MUST NOT|MUST|SHOULD NOT|SHOULD)\b`)

// anchorPattern is the stable identity a statement carries in the page:
// an HTML comment at the end of its line, invisible in rendered docs.
var anchorPattern = regexp.MustCompile(`<!-- tck: (.+?) -->`)

// covers maps a check's requirement id to further anchored statements the
// check's observations judge. A check id equal to an anchor covers that
// anchor by itself; this map is for the rest — one observation often
// judges several statements, and one statement is often judged from
// several angles.
var covers = map[string][]string{
	"host-mount@1/isolated-by-kit":                {"host-mount@1/identity-not-self-declared"},
	"host-mount@1/host-volume-conflict":           {"host-mount@1/no-silent-merge"},
	"agent-skill@1/host-conflict":                 {"agent-skill@1/existing-conflict"},
	"git-identity@1/global-defaults":              {"git-identity@1/runtime-provided"},
	"git-identity@1/unavailable-refuses-required": {"git-identity@1/runtime-provided"},
	"SPEC-v3 §7.1.1/conflicts":                    {"lifecycle@1/shared-paths-avoided"},
	"SPEC-v3 §7.1.1/atomic-selection":             {"SPEC-v3 §7.1.1/order", "SPEC-v3 §7.1.1/provenance"},
	// Reading the staged body behind the profile's reference judges what
	// the profile is; full progressive semantics are waived until a
	// fixture can observe them.
	"agent-context@1/directory-honored": {"agent-context@1/existing-content-preserved", "agent-context@1/explicit-profile-precedence"},
	"agent-context@1/body-readable": {
		"agent-context@1/workload-filename-is-profile",
	},

	// The access bound is judged from all four directions, and the
	// remaining skills checks observe the mount the first statement
	// requires.
	"agent-skills@1/host-readonly-narrows":             {"agent-skills@1/access-narrower-of-both"},
	"agent-skills@1/readwrite-granted-when-both-allow": {"agent-skills@1/access-narrower-of-both"},
	"agent-skills@1/readonly-default-honored":          {"agent-skills@1/access-narrower-of-both"},
	"agent-skills@1/shared-path-widest-mode":           {"agent-skills@1/store-mounted-at-declared-path"},
	"agent-skills@1/same-store-at-every-path":          {"agent-skills@1/store-mounted-at-declared-path"},
	"agent-skills@1/host-off-keeps-optional":           {"agent-skills@1/host-store-optional"},

	"agent-skills@1/host-store-missing":         {"agent-skills@1/host-store-optional"},
	"agent-skills@1/host-store-empty":           {"agent-skills@1/host-store-optional"},
	"agent-skills@1/bundled-with-missing-store": {"agent-skills@1/host-store-optional", "agent-skills@1/destination", "agent-skill@1/exposed", "agent-skill@1/before-launch"},
	"agent-skills@1/bundled-with-empty-store":   {"agent-skills@1/host-store-optional", "agent-skills@1/destination", "agent-skill@1/exposed", "agent-skill@1/before-launch"},

	// The check requires a non-empty sentinel where the secret is absent.
	"credential@1/secret-absent-in-sandbox": {"credential@1/sentinel-not-empty"},

	// The login check binds a session to a server whose host key the
	// runtime was never given, validly signed: only matching bindings to
	// keys from outside the sandbox refuses that login.

	// The hook env check judges names against the declared set and values
	// against the host sentinel; the files fixture's content is an arg
	// reference, so files-written observes expansion too.
	"lifecycle@1/hook-env-restricted": {"lifecycle@1/baseline-values-sandbox-derived"},
	"lifecycle@1/files-written":       {"lifecycle@1/args-expanded"},

	// The install-egress recording observes the same boundary lifecycle
	// requires the runtime to hold during install.
	"network-policy@1/install-phase-scoped": {"lifecycle@1/install-network-scope-held"},

	// @2 inherits the host-list duties from @1 and its checks re-judge
	// them under the new grammar; the HTTP checks observe the bounded
	// host and require the 403 the page mandates.
	"network-policy@2/deny-by-default":      {"network-policy@1/deny-by-default"},
	"network-policy@2/install-phase-scoped": {"network-policy@1/install-phase-scoped", "lifecycle@1/install-network-scope-held"},
	"network-policy@2/http-method-enforced": {"network-policy@2/bounded-host-refused-outside-rules", "network-policy@2/refusal-is-403"},
	"network-policy@2/http-path-enforced":   {"network-policy@2/bounded-host-refused-outside-rules", "network-policy@2/refusal-is-403"},
	"network-policy@2/http-deny-precedence": {"network-policy@2/refusal-is-403"},

	// The refusal probe composes one required fixture per unclaimed
	// well-known type, which is lifecycle's refusal duty exercised for
	// every claimant.
	"conformance.md §2.2/required-unclaimed-refused": {"lifecycle@1/required-unsatisfiable-refused", "long-running@1/required-unsatisfiable-refused"},

	// The writable-layer control distinguishes persistence from keeping
	// the old container; composition changes exercise reattachment too.
	"volume@1/persists-across-recreate": {"volume@1/persists-across-restart"},
	"volume@1/composition-independent":  {"volume@1/reattach-preserves-state"},
}

// kitCovers maps a kit-suite CHECK, by its unique name, onto the anchored
// statements that check's evidence judges. Keyed by check rather than by
// requirement id, because several checks share one section-level id and a
// section key would survive the deletion of the one check that actually
// supplies the evidence.
var kitCovers = map[string][]string{
	"agent-skill-content": {"agent-skill@1/content-present"},
	"oci-annotations":     {"SPEC-v3 §9.3/created-rfc3339"},
	// ValidatePublished enforces §9.2 on every descriptor this check
	// decodes, and refuses the authoring-only kind: set — a published
	// set that was never merged describes layers it does not have.
	"descriptor-valid": {
		"host-mount@1/runtime-owned-location",
		"agent-skill@1/name-valid",
		"agent-skill@1/source-literal",
		"long-running@1/no-config",
		"git-identity@1/no-config",
		"SPEC-v3 §7.1.1/group-grammar",
		"SPEC-v3 §7.1.1/validate-declarations",
		"SPEC-v3 §9.2/versioned-provides",
		"SPEC-v3 §3.4/set-kind-never-published",
	},

	// A merged kit's staged roots are counted against the kits its
	// descriptor records, which is the only reading of §10's duty an
	// artifact can be held to from outside.
	"merged-set": {"SPEC-v3 §10/merged-set-carries-sources"},

	// A mixin's layers are its overlay, so what they carry at the home
	// levels and where their links point are read straight off them.
	"overlay-home-ownership": {"SPEC-v3 §10/overlay-home-ownership"},
	"overlay-links-resolve":  {"SPEC-v3 §10/overlay-links-resolve"},

	// The floor a workload promises by declaring sbx@1 is a property of
	// the artifact, so the kit suite is where it is judged: the shells
	// the host runs things through, and an identity it can resolve out
	// of the image before the container exists. For the shells that is
	// presence, kind, and permission for the declared user; behaving
	// like a shell is not something an artifact can be asked, and shows
	// up wherever the host uses one.
	"sbx-platform-floor": {
		"sbx@1/posix-shell-present",
		"sbx@1/bash-present",
		"sbx@1/image-declares-user",
		"sbx@1/user-resolves-in-passwd",
	},
	"sbx-persistent-env": {"sbx@1/bash-env-names-a-shipped-file"},

	// The derivation ships its own evidence: the databases those entries
	// were read from are in the artifact, so each one is held back to
	// the filesystem that produced it — the package installed, and the
	// version the one §9.6 says to publish.
	"derived-provides": {
		"SPEC-v3 §9.6/workload-only",
		"SPEC-v3 §9.6/installed-only",
		"SPEC-v3 §9.6/version-is-upstream-core",
		// The same comparison judges it: a dropped prerelease is an
		// entry that is not there, and an entry that IS there is held
		// to PackageVersion, which refuses to name one.
		"SPEC-v3 §9.6/prerelease-dropped",
	},

	// Fetching the kits a set lists is what turns the merge from
	// something only its producer could check into something the
	// published artifact can be held to.
	"merged-set-declarations": {
		"SPEC-v3 §9.5/internal-requires-dropped",
		"SPEC-v3 §9.5/licenses-union",
		// The merged descriptor keeps kits[].args and the set's own
		// args, and each kit's declarations come from the digest it
		// is pinned to — so the contract is checkable after all.
		"SPEC-v3 §9.5/re-export-restates-contract",
		// Bound to the kits themselves, not merely counted.
		"SPEC-v3 §10/merged-set-carries-sources",
	},
}

// waived records statements the suite deliberately does not judge, each
// with its reason. A waiver is a decision, not a gap: it is reviewed like
// any other line, and removing one is how coverage grows.
var waived = map[string]string{
	// ACP protocol and host duties need a duplex client connection contract.
	"acp@1/optional-for-compatibility": "authoring recommendation, judged for the shipped ACP examples by TestACPExamples",
	"acp@1/config-valid":               "grammar rule, judged by TestACPValidation and TestACPSchema",
	"acp@1/agent-unique":               "grammar rule, judged by TestACPAgentUniqueness, including normalized names and expansion",
	"acp@1/sandbox-launch":             "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/provided-agent":             "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/negotiated-features":        "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/no-host-io":                 "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/session-identity":           "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/single-driver":              "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/declaration-grants-nothing": "permission projection rule, judged by TestACPSurface; the runtime adapter has no ACP host operation",
	"acp@1/transport":                  "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/noninteractive-auth":        "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/native-sessions":            "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/load-session":               "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/sandbox-io":                 "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/mcp-servers":                "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/permission-posture":         "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/model-effort":               "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/background-work":            "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/background-work-v2":         "the adapter contract has no duplex ACP host connection operation or ACP 2 fixture; background terminal output, exit status, and sub-agent completion while idle cannot be observed",
	"acp@1/shutdown":                   "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/concurrency":                "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/hook-session-id":            "the adapter contract has no duplex ACP host connection operation; completed exec results cannot observe this protocol or host-session duty",
	"acp@1/composition":                "composition rule, judged by TestACPComposition, including sets and selected groups",

	"git-identity@1/source-private":               "the adapter supplies an identity binding but exposes no mapping from its host source or translated backend to guest paths; effective Git probes cannot detect a readable copy at an arbitrary unconfigured path, and a bounded guest scan cannot prove its absence",
	"SPEC-v3 §7.1.1/selection-before-application": "the adapter exposes only final files, records, and grants, not an effect trace during selection; effects applied before selection and then rolled back cannot be observed",
	"SPEC-v3 §7.1.1/conflicts-before-application": "a refused create returns no sandbox ID and the adapter exposes no failed-create effect trace; the suite can observe conflict refusal but cannot inspect files or hooks applied before that refusal",
	"SPEC-v3 §7.1.1/publishing":                   "publisher ordering, provenance, and separate conditional context staging are judged by spec group publication tests and frontend end-to-end tests; the runtime adapter cannot compare original build inputs with the published artifact",
	// SPEC-v3: composition is the resolver's contract, judged by the
	// resolve package's own tests; the gate is host UX over surface
	// diffs, judged by the spec package's tests; authoring rules are the
	// frontend's to refuse at build.
	"SPEC-v3 §3.3/comment-descriptor-no-content":      "frontend build validation, not runtime behavior",
	"SPEC-v3 §3.5/workload-base-should-match-runtime": "SHOULD; authoring guidance no probe can judge",
	"SPEC-v3 §5.3/closed-set":                         "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §5.3/conflicts-fail":                     "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §5.3/integrates-violation-fails":         "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §5.3/dependency-order":                   "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §5.3/one-workload":                       "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §5.3/one-provider-per-name":              "resolver contract, judged by the resolve package's tests",
	"SPEC-v3 §7.4/widenings-gate":                     "gate semantics are surface diffs, judged by the spec package's tests",
	"SPEC-v3 §7/name-display-only":                    "identity, arity, surface, and merge behavior are judged by spec TestCapabilityNameDoesNotChangeIdentityOrSurface, TestMergeCapabilityNames, and TestCapabilityNamesDoNotHideMergeConflicts; no runtime probe observes whether a label changes execution",
	"SPEC-v3 §7/name-not-identity":                    "the adapter exposes no selection-record identity or structured diagnostic identity to distinguish same-named entries",
	"SPEC-v3 §7/name-round-trip":                      "decoding and serialization contract, judged by spec TestCapabilityNameRoundTrip and TestCapabilityNameRejectsNonStrings and the schema pin",
	"SPEC-v3 §9.5/capability-name-first":              "publish-time merge contract, judged by spec TestMergeCapabilityNames; a runtime probe cannot observe the original contributions' labels",
	"SPEC-v3 §6/expanded-key-collision-rejected":      "expansion semantics, judged by the spec package's ExpandCreateArgs tests",
	"SPEC-v3 §7.3/unknown-optional-skipped":           "no runtime probe yet; the fake's unclaimed-skip test judges the suite's own semantics, not a runtime",
	"SPEC-v3 §9.3/manifest-fallback":                  "consumer behavior; the kit checks verify fallback data exists, not that a consumer uses it",
	"SPEC-v3 §9.3/created-not-wall-clock":             "the source of a timestamp cannot be observed from the artifact; the kit check judges RFC 3339 format but cannot distinguish SOURCE_DATE_EPOCH from the wall clock",
	"SPEC-v3 §3.5/workload-has-content":               "no kit check distinguishes content layers from the staged-sources layer yet",

	// A set is merged at publish: what the rules judge is the frontend's
	// arithmetic over descriptors it read, which the spec package's
	// Merge tests judge statement by statement. The published artifact
	// carries no trace of its kits' own declarations to compare
	// against, so no runtime probe can reach them.
	"SPEC-v3 §3.4/set-has-kits":                      "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §3.4/set-declares-no-recipe":            "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §3.4/kit-published-reference":           "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §9.5/layers-preserved":                  "build-time layer arithmetic; llb.Merge preserves the inputs' layers, which the published manifest cannot distinguish from a repack",
	"SPEC-v3 §9.5/declarations-platform-independent": "the frontend holds every platform's merge to one descriptor at build; a single published manifest cannot show the comparison",

	// Derivation: what the entries say is checkable against the databases
	// that ship beside them, but agreement BETWEEN platforms is not — the
	// kit suite judges one platform manifest at a time, and the reconciled
	// entry is the only trace the comparison leaves.
	"SPEC-v3 §9.6/agreed-across-platforms":     "the frontend reconciles every platform's databases at build; one published manifest cannot show what another platform recorded",
	"SPEC-v3 §5.1/derived-namespaces-reserved": "grammar rule, judged by the spec package's validators; a published descriptor cannot distinguish an authored deb/ entry from a derived one, and derived-provides refuses either if the image disagrees",
	"SPEC-v3 §5.1/namespace-reverse-dns":       "grammar rule, judged by the spec package's validators and schema tests",

	// Grammar rules: the spec package's validators and schema tests are
	// where these are judged, statement by statement.
	"SPEC-v3 §4/schema-version-3":          "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §4/licenses-spdx":             "SHOULD; authoring guidance, schema-documented",
	"SPEC-v3 §4/dockerfile-inside-context": "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §5/requires-literal":          "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §6/arg-key-grammar":           "grammar rule, judged by the spec package's validators and schema tests",
	"SPEC-v3 §6/required-arg-supplied":     "grammar rule, judged by the spec package's expansion tests",
	"SPEC-v3 §6/references-declared":       "grammar rule, judged by the spec package's expansion tests",

	"agent-sessions@1/prompt-placeholder-required":                           "grammar rule, judged by the spec package's validators and schema tests",
	"agent-sessions@1/session-id-placeholder-required":                       "grammar rule, judged by the spec package's validators and schema tests",
	"agent-interactive-sessions@1/prompt-placeholder-required":               "grammar rule, judged by the spec package's validators and schema tests",
	"agent-interactive-sessions@1/session-id-placeholder-required":           "grammar rule, judged by the spec package's validators and schema tests",
	"agent-interactive-sessions@1/new-session-matches-lifecycle-interactive": "grammar rule, judged by the spec package's validator",
	"agent-interactive-sessions@1/list-matches-agent-sessions":               "grammar rule, judged by the spec package's validator",
	"credential@1/one-of-apikey-oauth":                                       "grammar rule, judged by the spec package's validators and schema tests",
	"credential@1/name-or-inject":                                            "grammar rule, judged by the spec package's validators and schema tests",
	"credential@1/inject-domain-in-allow":                                    "grammar rule, judged by the spec package's validators and schema tests",
	"network-policy@1/inject-domain-in-allow":                                "grammar rule, judged by the spec package's validators and schema tests",
	"network-policy@2/inject-domain-in-allow":                                "grammar rule, judged by the spec package's validators and schema tests",
	"network-policy@2/bounded-allow-hosts-literal":                           "grammar rule, judged by the spec package's validators and schema tests",

	// Kit-author obligations bind kit authors, not the runtime under
	// test; there is no runtime behavior to probe.
	"lifecycle@1/startup-idempotent-authors": "kit-author obligation, not runtime behavior",
	"privileged@1/description-says-why":      "kit-author obligation, not runtime behavior",
	"resources@1/workload-owns-declaration":  "kit-author obligation, not runtime behavior",
	"agent-context@1/content-neutralized":    "SHOULD; neutralization has no reliable in-sandbox symptom",

	"lifecycle@1/install-before-entrypoint": "needs an entrypoint that records what it observed at first run; no fixture yet",
	"lifecycle@1/hooks-dependency-order":    "needs a two-kit ordered-hooks fixture; not exercised yet",
	"lifecycle@1/files-overwrite-honored":   "needs a fixture whose image carries the target file; not exercised yet",
	"lifecycle@1/files-before-entrypoint":   "needs an entrypoint that records what it observed at first run; no fixture yet",

	"kit-registry@1/content-must-not-assume-address": "the facade is reference-implementation infrastructure, not portable behavior",
	"port@1/host-binding-default":                    "the adapter contract offers no host-side observation, so a published mapping cannot be seen",
	"privileged@1/widening-gates":                    "gate semantics are surface diffs, judged by the spec package's tests",
	"resources@1/max-of-declarations":                "needs a multi-declaration fixture; not exercised yet",
	"resources@1/never-undercut-workload":            "needs a multi-declaration fixture; not exercised yet",

	// Statements about what a runtime must not trust or must keep to
	// itself are unobservable from inside a sandbox.
	"agent-context@1/content-untrusted":              "non-observable: distrust has no in-sandbox symptom",
	"agent-context@1/progressive-surfacing":          "the body-readable check observes one mixin's reference; every-kit attribution and no-inlining need a multi-mixin fixture",
	"lifecycle@1/install-credentials-end-with-phase": "named proxy-managed API-key sentinels are probed, but revocation of outbound injection and OAuth presentations still needs fixture services",
	"SPEC-v3 §4/sandbox-spelling-not-written":        "kit-author obligation, not runtime behavior",
	"agent-skills@1/store-untrusted":                 "non-observable: distrust has no in-sandbox symptom",

	"agent-sessions@1/headless-from-launch-argv":  "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-sessions@1/raw-value-substitution":     "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-sessions@1/list-parses-stdout":         "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-sessions@1/absent-verb-unsupported":    "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-sessions@1/declaration-grants-nothing": "grammar only; nothing consumes it yet, so there is no behavior to judge",

	"agent-interactive-sessions@1/interactive-from-launch-argv":           "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/absent-new-session-is-lifecycle-launch": "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/terminal-attached":                      "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/raw-value-substitution":                 "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/list-parses-stdout":                     "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/absent-verb-unsupported":                "grammar only; nothing consumes it yet, so there is no behavior to judge",
	"agent-interactive-sessions@1/declaration-grants-nothing":             "grammar only; nothing consumes it yet, so there is no behavior to judge",

	"credential@1/resolved-from-host-store":         "host-internal: which store the value came from has no in-sandbox symptom",
	"credential@1/oauth-token-endpoint-intercepted": "needs an OAuth fixture service the suite does not run yet",
	"credential@1/phase-scoped":                     "sentinel-phase-scoped observes only named proxy-managed API-key environments; outbound injection, OAuth endpoints and files, and unproxied credentials need presentation-specific phase probes",
	"credential@1/required-without-binding-fails":   "the contract has the adapter bind the fixture secret, so the unbound path never occurs in-suite",

	"ssh-agent@1/key-material-outside-sandbox": "non-observable: a key copied somewhere in the sandbox has no symptom a probe could search for; the relay filter check shows keys cannot be added or exported through the agent",
	"ssh-agent@1/grant-names-scope":            "SHOULD; consent wording, not sandbox behavior",
	"ssh-agent@1/keys-selectable":              "SHOULD; a user-side setting the adapter contract offers no input for",
	"ssh-agent@1/confirmation-offered":         "SHOULD; asks the user, which a non-interactive suite cannot answer",
	"ssh-agent@1/grant-expiry-offered":         "SHOULD; a user-side setting the adapter contract offers no input for",
	"ssh-agent@1/signatures-observable":        "SHOULD; where a runtime records signatures is host-side and unspecified",

	"kit-registry@1/no-route-unless-requested": "the facade is reference-implementation infrastructure, not portable behavior",
	"kit-registry@1/endpoint-announced":        "the facade is reference-implementation infrastructure, not portable behavior",
	"kit-registry@1/grant-scoped-to-endpoint":  "the facade is reference-implementation infrastructure, not portable behavior",
	"kit-registry@1/namespace-discipline":      "the facade is reference-implementation infrastructure, not portable behavior",
	"kit-registry@1/serves-kit-content-only":   "the facade is reference-implementation infrastructure, not portable behavior",

	"lifecycle@1/default-users":           "SHOULD; needs per-phase identity probes the fixtures do not carry yet",
	"lifecycle@1/interactive-launch":      "the adapter contract has no TTY verb, deliberately",
	"lifecycle@1/failing-hook-attributed": "SHOULD; error-message shape, not sandbox behavior",

	"network-policy@1/deny-precedence":       "needs a fixture host on both lists; not exercised yet",
	"network-policy@1/unbypassable-boundary": "non-observable from inside: a bypass is invisible to the sandbox that found it",
	"network-policy@1/refusals-observable":   "SHOULD; log shape is host-side and unspecified",

	"network-policy@2/omitted-methods-paths-are-every": "grammar expansion, judged by the spec package's surface tests",
	"network-policy@2/fail-closed-on-uninspectable":    "needs a TLS-uninspectable fixture endpoint the suite does not run yet",
	"sbx@1/agent-launched-under-bash":                  "the adapter contract has no attach verb, so the suite cannot observe how the agent itself is launched — only exec, which is not that",
	"network-policy@2/unbypassable-boundary":           "non-observable from inside, as in @1",
	"network-policy@2/refused-rule-observable":         "SHOULD; log shape is host-side and unspecified",

	// Publishing a port is observable from the host, and the contract's
	// verbs only reach inside the sandbox. Lifting these means giving
	// the adapter a verb that reports the mapping — a conformance.md
	// change, not a suite change.
	"port@1/published-when-granted":      "the adapter contract offers no host-side observation, so a published mapping cannot be seen",
	"port@1/host-side-allocated-by-host": "the adapter contract offers no host-side observation, so a published mapping cannot be seen",
	"port@1/name-surfaced":               "SHOULD; display surface, not sandbox behavior",

	"privileged@1/explicit-consent": "SHOULD; consent flows are host UX the adapter hides",

	"resources@1/no-silent-truncation": "truncation and a smaller honest limit are indistinguishable from inside; the limit check reports mismatches",

	"usb-device@1/matching-devices-passed": "needs host hardware the suite cannot assume",
	"usb-device@1/grant-scoped-to-match":   "needs host hardware the suite cannot assume",

	"volume@1/tmpfs-ram-backed":      "RAM-backing has no reliable in-sandbox symptom",
	"volume@1/size-and-mode-applied": "SHOULD; the suite probes reattached permissions but does not independently judge initial permissions or capacity enforcement",
	"volume@1/agent-writable-root":   "SHOULD; data probes need usable mounts but no check independently reports initial root ownership or writability",
}

// specDocs are the documents whose normative statements the guard
// accounts for. conformance.md is deliberately absent: it binds adapters
// and this suite itself, and the harness enforces it by construction.
func specDocs(t *testing.T) map[string][]byte {
	t.Helper()
	root := filepath.Join("..", "..", "docs", "spec")
	docs := map[string][]byte{}
	body, err := os.ReadFile(filepath.Join(root, "SPEC-v3.md"))
	require.NoError(t, err)
	docs["SPEC-v3.md"] = body

	dir := filepath.Join(root, "capabilities", "com.docker.sandbox")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "no capability pages found; the guard would pass vacuously")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		docs[e.Name()] = body
	}
	return docs
}

// anchors returns every statement id the documents declare, failing on
// duplicates: an anchor is an identity, and two statements sharing one
// would let a check claim both.
func anchors(t *testing.T, docs map[string][]byte) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for doc, body := range docs {
		for _, m := range anchorPattern.FindAllStringSubmatch(string(body), -1) {
			require.False(t, ids[m[1]], "anchor %q declared twice (second in %s)", m[1], doc)
			ids[m[1]] = true
		}
	}
	return ids
}

// TestEveryNormativeLineCarriesAnAnchor is the drift catcher: a new MUST
// or SHOULD added to a page without an anchor fails here, so a statement
// cannot enter the specification without entering the accounting.
func TestEveryNormativeLineCarriesAnAnchor(t *testing.T) {
	var missing []string
	for doc, body := range specDocs(t) {
		inFence := false
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			// Code blocks are examples; comments inside them may quote a
			// keyword without stating a requirement — except the ones we
			// anchored on purpose, which stay accountable.
			if inFence && !anchorPattern.MatchString(line) {
				continue
			}
			if !normative.MatchString(line) {
				continue
			}
			// The RFC 2119 boilerplate names the key words without
			// stating a requirement.
			if strings.Contains(line, "The key words") || strings.Contains(strings.ToLower(line), "rfc 2119") {
				continue
			}
			// One statement per line is the premise the anchors rest on:
			// a second clause appended to an anchored line would ride the
			// first clause's accounting, so it has to become its own line
			// before it can enter the spec.
			if len(normative.FindAllString(line, -1)) > 1 {
				missing = append(missing, doc+" (two statements on one line; split them): "+strings.TrimSpace(line))
				continue
			}
			// Continuation lines of an anchored statement: the anchor
			// sits on the line that opens the statement, and a wrapped
			// bullet may repeat a keyword mid-sentence. Only lines that
			// OPEN a statement (a bullet, a table row, or a paragraph
			// start) must carry their own anchor; a continuation is
			// attributed to the previous anchored line.
			if anchorPattern.MatchString(line) {
				continue
			}
			lines := strings.Split(string(body), "\n")
			opens := strings.HasPrefix(strings.TrimSpace(line), "- ") ||
				strings.HasPrefix(strings.TrimSpace(line), "|")
			if i > 0 && anchorPattern.MatchString(lines[i-1]) && !opens {
				continue
			}
			missing = append(missing, doc+": "+strings.TrimSpace(line))
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"normative statements without a tck anchor; add <!-- tck: <id> --> and a check or waiver:\n%s",
		strings.Join(missing, "\n"))
}

// TestEveryStatementIsCheckedOrWaived is the conformance claim itself:
// each anchored statement is judged by at least one check — by id or
// through the covers maps — or carries an explicit waiver with a reason.
func TestEveryStatementIsCheckedOrWaived(t *testing.T) {
	ids := anchors(t, specDocs(t))

	covered := map[string]bool{}
	for _, r := range Requirements() {
		covered[r] = true
		for _, a := range covers[r] {
			covered[a] = true
		}
	}
	for _, extra := range kitCovers {
		for _, a := range extra {
			covered[a] = true
		}
	}

	var unaccounted []string
	for id := range ids {
		if !covered[id] && waived[id] == "" {
			unaccounted = append(unaccounted, id)
		}
	}
	sort.Strings(unaccounted)
	require.Empty(t, unaccounted,
		"anchored statements with neither a check nor a waiver:\n%s",
		strings.Join(unaccounted, "\n"))
}

// TestTheAccountingNamesRealThings guards the guard: waivers and covers
// entries must reference anchors that exist, runtime requirement ids must
// be anchors or covers keys, and nothing may be both waived and covered —
// a waiver for a judged statement is a stale decision hiding progress.
func TestTheAccountingNamesRealThings(t *testing.T) {
	ids := anchors(t, specDocs(t))

	for id := range waived {
		require.True(t, ids[id], "waiver names %q, which is not an anchored statement", id)
	}
	for from, tos := range covers {
		for _, to := range tos {
			require.True(t, ids[to], "covers[%q] names %q, which is not an anchored statement", from, to)
		}
	}
	kitIDs := map[string]bool{}
	for _, name := range tckkit.CheckNames() {
		kitIDs[name] = true
	}
	for from, tos := range kitCovers {
		require.True(t, kitIDs[from], "kitCovers key %q is not a kit-suite check name", from)
		for _, to := range tos {
			require.True(t, ids[to], "kitCovers names %q, which is not an anchored statement", to)
		}
	}

	covered := map[string]bool{}
	for _, r := range Requirements() {
		// conformance.md ids bind the adapter contract, which is outside
		// the anchored scope; everything else must point at a statement.
		if !strings.HasPrefix(r, "conformance.md ") {
			require.True(t, ids[r] || len(covers[r]) > 0,
				"check requirement %q matches no anchored statement and no covers entry", r)
		}
		covered[r] = true
		for _, a := range covers[r] {
			covered[a] = true
		}
	}
	for _, tos := range kitCovers {
		for _, a := range tos {
			covered[a] = true
		}
	}
	for id := range waived {
		require.False(t, covered[id], "%q is both waived and covered; drop the waiver", id)
	}
}

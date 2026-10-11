package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MCP transports a server Kit's entrypoint can speak. SSE is absent on
// purpose: the MCP specification deprecated it in favor of streamable
// HTTP, and a version of this capability that admits it cannot drop it.
const (
	MCPTransportStdio          = "stdio"
	MCPTransportStreamableHTTP = "streamable-http"

	// DefaultMCPPath is where a streamable-HTTP server is reached when
	// the declaration names no path.
	DefaultMCPPath = "/mcp"
)

// MCPTransports lists the transports in the order the schema states them.
func MCPTransports() []string {
	return []string{MCPTransportStdio, MCPTransportStreamableHTTP}
}

var (
	// mcpServerName is one DNS label: the name doubles as a gateway
	// routing and tool-prefix key, where dots and case would be ambiguous.
	mcpServerName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	// mcpServerPath is an absolute URL path built from RFC 3986 pchars:
	// no query, fragment, or whitespace, which a gateway would otherwise
	// have to re-encode or silently drop.
	mcpServerPath = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)
)

// MCPServer is CapabilityMCPServer's config. Everything else about how
// the server starts — entrypoint, environment, user, working directory —
// is the image config's, as for any workload.
type MCPServer struct {
	Name      string `json:"name" yaml:"name"`
	Transport string `json:"transport" yaml:"transport"`
	Port      *int   `json:"port,omitempty" yaml:"port,omitempty"`
	Path      string `json:"path,omitempty" yaml:"path,omitempty"`
	// Arguments replaces the image's cmd, as `docker run <image> <args>`
	// does: the placement the MCP Registry's packageArguments and Docker's
	// catalog command both assume for an OCI server.
	Arguments MCPArguments `json:"arguments,omitempty" yaml:"arguments,omitempty"`
}

// MCPArguments is an argv tail. Each element is text, but a whole-value
// arg reference adopts its value's type at create (SPEC-v3 §6), so a
// number or boolean decodes back to the spelling it expanded from:
// typedScalar adopts only spellings that render back identically, and
// json.Number keeps an integer exact past float64's precision.
type MCPArguments []string

func (a *MCPArguments) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("arguments must be a list of strings")
	}
	out := make(MCPArguments, 0, len(raw))
	for i, r := range raw {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("arguments[%d]: %w", i, err)
		}
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case bool:
			out = append(out, strconv.FormatBool(v))
		case json.Number:
			if _, err := v.Int64(); err == nil {
				out = append(out, v.String())
				continue
			}
			f, err := v.Float64()
			if err != nil {
				return fmt.Errorf("arguments[%d]: %w", i, err)
			}
			out = append(out, strconv.FormatFloat(f, 'f', -1, 64))
		default:
			return fmt.Errorf("arguments[%d] must be a string", i)
		}
	}
	*a = out
	return nil
}

// MCPServerPath returns the declared HTTP path or its default. It is
// meaningless for a stdio server.
func MCPServerPath(m MCPServer) string {
	if m.Path == "" {
		return DefaultMCPPath
	}
	return m.Path
}

// MCPServerOf returns the server declaration among selected capabilities,
// or nil when the Kit is not an MCP server.
func MCPServerOf(capabilities []Capability) (*MCPServer, error) {
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("mcp-server: select groups first")
		}
		if c.Type != CapabilityMCPServer {
			continue
		}
		var m MCPServer
		if err := DecodeCapabilityConfig(c, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
	return nil, nil
}

// mcpServerCompanions are the well-known types a server Kit may declare
// beside its mcp-server@1 entry: the ones that bound the server's own
// container. Every other well-known type describes a sandbox — an agent,
// its sessions, its filesystem — and a host never builds one from this
// Kit, so declaring it would promise something no runtime delivers.
var mcpServerCompanions = []string{
	CapabilityMCPServer,
	CapabilityNetworkPolicy,
	CapabilityNetworkPolicyV2,
	CapabilityCredential,
	CapabilityResources,
}

// validateMCPServerRole holds a Kit declaring mcp-server@1 to the server
// role across the whole capability list, group members included: the
// declaration changes what the image is, so it cannot be an alternative a
// host selects, an ask a host may skip, or an overlay on someone else's
// root filesystem.
func validateMCPServerRole(d *Descriptor) error {
	if !HasCapability(DeclaredCapabilities(d.Capabilities), CapabilityMCPServer) {
		return nil
	}
	var errs ValidationErrors
	if d.Kind == KindMixin {
		errs.add(fieldErrorf("kind", "%s is workload-only: a mixin's layers overlay another image, so its entrypoint is never the one a host would start", CapabilityMCPServer))
	}
	known := KnownCapabilities()
	check := func(path string, n Capability, member bool) {
		if n.Type == CapabilityMCPServer {
			if member {
				errs.add(fieldErrorf(path, "%s must not be a group member: whether the image is a server is not a selectable alternative", CapabilityMCPServer))
			}
			if n.Optional {
				errs.add(fieldErrorf(path+".optional", "%s must not be optional: a host that skipped it would start the server as a sandbox", CapabilityMCPServer))
			}
			return
		}
		if slices.Contains(known, n.Type) && !slices.Contains(mcpServerCompanions, n.Type) {
			errs.add(fieldErrorf(path+".type", "%s does not apply to a Kit declaring %s: the host attaches the server to a sandbox's MCP gateway and never builds a sandbox from it", n.Type, CapabilityMCPServer))
		}
	}
	for i, c := range d.Capabilities {
		path := fmt.Sprintf("capabilities[%d]", i)
		if c.Group == nil {
			check(path, c, false)
			continue
		}
		for j, member := range c.Group.Capabilities {
			check(fmt.Sprintf("%s.group.capabilities[%d]", path, j), member, true)
		}
	}
	return errs.err()
}

// validateMCPServerAuthored holds the rules that do not wait for decoding:
// which fields are present, and which may be parameterized. The endpoint
// fields describe what the image's entrypoint serves, which no arg can
// change, and a gateway reads them from the published artifact before
// anything is attached. Arguments are the installer's configuration of
// that entrypoint, so they take args — but not the container
// environment, which the entrypoint already reads for itself.
func validateMCPServerAuthored(path string, i int, n Capability) error {
	var errs ValidationErrors
	endpoint := Capability{Type: n.Type, Config: map[string]any{}}
	for key, value := range n.Config {
		if key != "arguments" {
			endpoint.Config[key] = value
		}
	}
	if capabilityIsParameterized(endpoint) {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: %s name, transport, port, and path must be literal: they describe what the image's entrypoint serves, which no arg can change", i, CapabilityMCPServer))
	}
	if list, ok := n.Config["arguments"].([]any); ok {
		for j, arg := range list {
			if s, ok := arg.(string); ok && ContainsEnvRef(s) {
				errs.add(fieldErrorf(fmt.Sprintf("%s.config.arguments[%d]", path, j), "capabilities[%d]: arguments may reference kit.args but not kit.env: the entrypoint reads its own environment directly", i))
			}
		}
	}
	for _, key := range []string{"name", "transport", "port", "path", "arguments"} {
		value, stated := n.Config[key]
		if !stated && (key == "name" || key == "transport") {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s is required", i, key))
		} else if stated && hasNull(value) {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s must not be null", i, key))
		}
	}
	if transport, _ := n.Config["transport"].(string); transport == MCPTransportStdio {
		for _, key := range []string{"port", "path"} {
			if _, stated := n.Config[key]; stated {
				errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s applies only to %s; a stdio server is reached on its standard streams", i, key, MCPTransportStreamableHTTP))
			}
		}
	}
	return errs.err()
}

func validateMCPServer(path string, i int, n Capability) error {
	var errs ValidationErrors
	errs.add(validateMCPServerAuthored(path, i, n))
	var m MCPServer
	if err := DecodeCapabilityConfig(n, &m); err != nil {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: %v", i, err))
		return errs.err()
	}
	if _, stated := n.Config["name"]; stated && !mcpServerName.MatchString(m.Name) {
		errs.add(fieldErrorf(path+".config.name", "capabilities[%d]: name %q must be one lowercase DNS label: letters, digits, and inner hyphens, at most 63 characters", i, m.Name))
	}
	if _, stated := n.Config["transport"]; stated && !slices.Contains(MCPTransports(), m.Transport) {
		errs.add(fieldErrorf(path+".config.transport", "capabilities[%d]: transport must be %q or %q, got %q", i, MCPTransportStdio, MCPTransportStreamableHTTP, m.Transport))
	}
	if _, stated := n.Config["arguments"]; stated && len(m.Arguments) == 0 && !hasNull(n.Config["arguments"]) {
		errs.add(fieldErrorf(path+".config.arguments", "capabilities[%d]: arguments must not be empty; omit it to run the image's cmd", i))
	}
	for j, arg := range m.Arguments {
		if strings.ContainsRune(arg, 0) {
			errs.add(fieldErrorf(fmt.Sprintf("%s.config.arguments[%d]", path, j), "capabilities[%d]: an argument must not contain NUL, which no argv can carry", i))
		}
	}
	if m.Transport == MCPTransportStreamableHTTP {
		if m.Port == nil {
			if _, stated := n.Config["port"]; !stated {
				errs.add(fieldErrorf(path+".config.port", "capabilities[%d]: port is required for %s: nothing in the image config says which exposed port speaks MCP", i, MCPTransportStreamableHTTP))
			}
		} else if *m.Port < 1 || *m.Port > 65535 {
			errs.add(fieldErrorf(path+".config.port", "capabilities[%d]: port must be 1-65535, got %d", i, *m.Port))
		}
		if _, stated := n.Config["path"]; stated && !mcpServerPath.MatchString(m.Path) {
			errs.add(fieldErrorf(path+".config.path", "capabilities[%d]: path %q must be an absolute URL path without query, fragment, or whitespace", i, m.Path))
		}
	}
	return errs.err()
}

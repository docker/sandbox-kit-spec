package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MCP transports a server in a sandbox can speak. SSE is absent on
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
	// have to re-encode or silently drop, and `%` only as a complete
	// escape, which a URL parser would otherwise refuse.
	mcpServerPath = regexp.MustCompile(`^/(?:[A-Za-z0-9._~!$&'()*+,;=:@/-]|%[0-9A-Fa-f]{2})*$`)
)

// MCPServer is CapabilityMCPServer's config: one MCP server a sandbox
// runs, and how an MCP client reaches it.
type MCPServer struct {
	Name      string `json:"name" yaml:"name"`
	Transport string `json:"transport" yaml:"transport"`
	Port      *int   `json:"port,omitempty" yaml:"port,omitempty"`
	Path      string `json:"path,omitempty" yaml:"path,omitempty"`
	// Command starts the server inside the sandbox. Without one, the
	// server is the workload's launch — §8's entrypoint plus cmd — which
	// is how a Kit packaging a single server says so without restating
	// its own entrypoint.
	Command CommandLine `json:"command,omitempty" yaml:"command,omitempty"`
	// Arguments replaces the launch's cmd, as `docker run <image> <args>`
	// does: the placement the MCP Registry's packageArguments and Docker's
	// catalog command both assume for an OCI server. A command entry has
	// no cmd to replace, so it states its whole argv in Command instead.
	Arguments MCPArguments `json:"arguments,omitempty" yaml:"arguments,omitempty"`
}

// MCPServerRunsLaunch reports whether the server is the workload's
// launch rather than a command of its own.
func MCPServerRunsLaunch(m MCPServer) bool {
	return len(m.Command) == 0
}

// MCPServerCapability retains the selection and display metadata of a
// server declaration.
// The server is a named field rather than embedded: both carry a Name,
// the server's for the gateway and the entry's for display.
type MCPServerCapability struct {
	Server      MCPServer
	Optional    bool
	Name        string
	Description string
}

// MCPArguments is an argv tail. Each element is text, but a whole-value
// arg reference adopts its value's type when it expands (SPEC-v3 §6),
// so a number or boolean decodes back to the spelling it expanded from:
// typedScalar adopts only spellings that render back identically, and
// json.Number keeps an integer exact past float64's precision.
//
// Build-phase expansion, create-phase expansion, and a set pinning its
// kits' args all hand adopted scalars to the validators an authored
// descriptor goes through, so decoding cannot refuse an authored
// literal without refusing them. The editor schema, which only ever
// sees authored text, admits strings alone.
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

// sameMCPServer reports whether two declarations name one endpoint and
// one launch, reading an omitted HTTP path as the default it stands for.
func sameMCPServer(a, b MCPServer) bool {
	for _, m := range []*MCPServer{&a, &b} {
		if m.Transport == MCPTransportStreamableHTTP {
			m.Path = MCPServerPath(*m)
		}
	}
	return reflect.DeepEqual(a, b)
}

// MCPServersOf returns the selected server declarations in declaration
// order.
func MCPServersOf(capabilities []Capability) ([]MCPServerCapability, error) {
	var out []MCPServerCapability
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
		out = append(out, MCPServerCapability{Server: m, Optional: c.Optional, Name: c.Name, Description: c.Description})
	}
	return out, nil
}

// validateMCPServerRole holds the rules that span a descriptor's server
// entries, group members included. A sandbox has one launch, so at most
// one entry can be it, and only a workload has a launch to offer: a
// mixin's entrypoint and cmd never take effect (§10), and an sbx@1
// workload's launch is its agent.
func validateMCPServerRole(d *Descriptor) error {
	var errs ValidationErrors
	agent := false
	for _, c := range d.Capabilities {
		agent = agent || c.Type == CapabilitySbx
		if c.Group != nil {
			for _, member := range c.Group.Capabilities {
				agent = agent || member.Type == CapabilitySbx
			}
		}
	}
	launch := ""
	names := map[string]string{}
	check := func(path string, n Capability) {
		if n.Type != CapabilityMCPServer {
			return
		}
		if _, stated := n.Config["command"]; !stated {
			if d.Kind == KindMixin {
				errs.add(fieldErrorf(path+".config", "%s without a command runs the workload's launch, and a mixin has none: give the entry a command", CapabilityMCPServer))
			}
			if agent {
				errs.add(fieldErrorf(path+".config", "%s without a command runs the workload's launch, and beside %s that launch is the agent: give the entry a command", CapabilityMCPServer, CapabilitySbx))
			}
			if launch != "" {
				errs.add(fieldErrorf(path+".config", "%s without a command runs the workload's launch, which %s already runs: a sandbox has one launch, so give one of them a command", CapabilityMCPServer, launch))
			} else {
				launch = path
			}
		}
		if name, ok := n.Config["name"].(string); ok && !ContainsArgRef(name) && !ContainsEnvRef(name) {
			if prev, dup := names[name]; dup {
				errs.add(fieldErrorf(path+".config.name", "%s %q already declared at %s", CapabilityMCPServer, name, prev))
			} else {
				names[name] = path
			}
		}
	}
	for i, c := range d.Capabilities {
		path := fmt.Sprintf("capabilities[%d]", i)
		if c.Group == nil {
			check(path, c)
			continue
		}
		for j, member := range c.Group.Capabilities {
			check(fmt.Sprintf("%s.group.capabilities[%d]", path, j), member)
		}
	}
	return errs.err()
}

// mcpServerEndpointFields are what a host lists and routes on — in a
// catalog, at an attach prompt — from the published artifact, before
// any sandbox resolves its args. They stay literal; how the server
// starts is the installer's to configure.
var mcpServerEndpointFields = []string{"name", "transport", "port", "path"}

// validateMCPServerAuthored holds the rules that do not wait for decoding:
// which fields are present, and which may be parameterized.
func validateMCPServerAuthored(path string, i int, n Capability) error {
	var errs ValidationErrors
	endpoint := Capability{Type: n.Type, Config: map[string]any{}}
	for _, key := range mcpServerEndpointFields {
		if value, stated := n.Config[key]; stated {
			endpoint.Config[key] = value
		}
	}
	if capabilityIsParameterized(endpoint) {
		errs.add(fieldErrorf(path+".config", "capabilities[%d]: %s name, transport, port, and path must be literal: a host lists and routes on them before any sandbox resolves its args", i, CapabilityMCPServer))
	}
	for _, key := range []string{"name", "transport", "port", "path", "command", "arguments"} {
		value, stated := n.Config[key]
		if !stated && (key == "name" || key == "transport") {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s is required", i, key))
		} else if stated && hasNull(value) {
			errs.add(fieldErrorf(path+".config."+key, "capabilities[%d]: %s must not be null", i, key))
		}
	}
	if command, stated := n.Config["command"]; stated && !hasNull(command) && emptyCommand(command) {
		errs.add(fieldErrorf(path+".config.command", "capabilities[%d]: command must name a command; omit it to run the workload's launch", i))
	}
	_, hasCommand := n.Config["command"]
	if _, hasArguments := n.Config["arguments"]; hasCommand && hasArguments {
		errs.add(fieldErrorf(path+".config.arguments", "capabilities[%d]: arguments replace the launch's cmd, and an entry with a command has none: state the whole argv in command", i))
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
		errs.add(fieldErrorf(path+".config.arguments", "capabilities[%d]: arguments must not be empty; omit it to run the launch's cmd", i))
	}
	for j, arg := range m.Arguments {
		if strings.ContainsRune(arg, 0) {
			errs.add(fieldErrorf(fmt.Sprintf("%s.config.arguments[%d]", path, j), "capabilities[%d]: an argument must not contain NUL, which no argv can carry", i))
		}
	}
	for _, arg := range m.Command {
		if strings.ContainsRune(arg, 0) {
			errs.add(fieldErrorf(path+".config.command", "capabilities[%d]: command contains NUL, which no argv can carry", i))
		}
	}
	if m.Transport == MCPTransportStreamableHTTP {
		if m.Port == nil {
			if _, stated := n.Config["port"]; !stated {
				errs.add(fieldErrorf(path+".config.port", "capabilities[%d]: port is required for %s: nothing else says which port in the sandbox speaks MCP", i, MCPTransportStreamableHTTP))
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

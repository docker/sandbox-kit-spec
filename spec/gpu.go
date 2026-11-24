package spec

import (
	"regexp"
	"strconv"
	"strings"
)

// gpuDriver is gpu@1's driver grammar: a lowercase DNS name, a slash, and
// a name. validGPUDriver adds the length limits RE2 cannot count.
var gpuDriver = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*/[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// gpuDevices admits "all", a positive count, or empty for the default. A
// digit string with a leading zero is refused so a count always reads as
// one number.
var gpuDevices = regexp.MustCompile(`^(all|[1-9][0-9]*)?$`)

func validGPUDriver(driver string) bool {
	domain, name, _ := strings.Cut(driver, "/")
	return gpuDriver.MatchString(driver) && len(domain) <= 253 && len(name) <= 63
}

// gpuSurface is the surface entry for one gpu@1 request: the driver and
// the effective devices, so an omitted default and its spelling agree.
func gpuSurface(g GPU) string {
	return g.Driver + " " + g.EffectiveDevices()
}

// gpuWidenings reports the candidate entries no granted entry covers. A
// grant covers a request of the same driver when it is "all", or when
// both are counts and the request's is not larger. Anything subtler
// reports as a widening, which errs toward gating.
func gpuWidenings(granted, candidate []string) []string {
	var out []string
	for _, c := range candidate {
		cDriver, cDevices, _ := strings.Cut(c, " ")
		covered := false
		for _, g := range granted {
			gDriver, gDevices, _ := strings.Cut(g, " ")
			if gDriver != cDriver {
				continue
			}
			if gDevices == GPUDevicesAll || gDevices == cDevices {
				covered = true
				break
			}
			gn, gErr := strconv.Atoi(gDevices)
			cn, cErr := strconv.Atoi(cDevices)
			if gErr == nil && cErr == nil && cn <= gn {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, c)
		}
	}
	return out
}

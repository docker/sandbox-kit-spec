#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <wayland-client.h>

static void global(void *data, struct wl_registry *registry, uint32_t name,
                   const char *interface, uint32_t version) {
    struct wl_compositor **compositor = data;
    if (!*compositor && version >= 1 && strcmp(interface, "wl_compositor") == 0)
        *compositor = wl_registry_bind(registry, name, &wl_compositor_interface, 1);
}

static void global_remove(void *data, struct wl_registry *registry, uint32_t name) {
    (void)data;
    (void)registry;
    (void)name;
}

int main(int argc, char **argv) {
    static const struct wl_registry_listener listener = {global, global_remove};
    struct wl_compositor *compositor = NULL;

    if (argc != 2 || argv[1][0] != '/') {
        fputs("expected an absolute socket path\n", stderr);
        return 1;
    }
    /* An inherited Wayland fd can point to an entirely different server. */
    unsetenv("WAYLAND_SOCKET");
    alarm(5);
    struct wl_display *display = wl_display_connect(argv[1]);
    if (!display) {
        if (errno == ENOENT || errno == EACCES || errno == ECONNREFUSED) {
            puts("unavailable");
            return 0;
        }
        perror("connect to the display socket");
        return 1;
    }

    struct wl_registry *registry = wl_display_get_registry(display);
    if (!registry || wl_registry_add_listener(registry, &listener, &compositor) < 0 ||
        wl_display_roundtrip(display) < 0 || !compositor) {
        fputs("could not bind wl_compositor\n", stderr);
        return 1;
    }
    struct wl_surface *surface = wl_compositor_create_surface(compositor);
    if (!surface)
        return 1;
    /* No buffer or window role: this exercises the surface without opening a window. */
    wl_surface_commit(surface);
    if (wl_display_roundtrip(display) < 0) {
        fputs("compositor rejected the surface\n", stderr);
        return 1;
    }
    wl_surface_destroy(surface);
    wl_compositor_destroy(compositor);
    wl_registry_destroy(registry);
    wl_display_disconnect(display);
    puts("usable");
    return 0;
}

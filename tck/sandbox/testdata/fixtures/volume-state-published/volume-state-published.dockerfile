FROM scratch
COPY --chmod=0444 <<'MARKER' /usr/share/kit-tck-volume-published
published
MARKER

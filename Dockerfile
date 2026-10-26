FROM dhi.io/golang:1.27-dev AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# The build stamp has to be handed down: .dockerignore excludes .git, so
# neither the linker flags nor Go's own VCS stamping can read it here. An
# unstamped build is a working frontend that calls itself "dev", which is
# what a local `docker build -t docker/sandbox-kit:3 .` should say.
ARG VERSION=""
ARG REVISION=""
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-s -w \
      -X github.com/docker/sandbox-kit-spec/v3/internal/version.Version=${VERSION} \
      -X github.com/docker/sandbox-kit-spec/v3/internal/version.Revision=${REVISION}" \
      -o /frontend ./cmd/frontend

FROM scratch
COPY --from=build /frontend /bin/frontend
ENTRYPOINT ["/bin/frontend"]

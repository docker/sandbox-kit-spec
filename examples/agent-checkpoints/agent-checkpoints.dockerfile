# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM dhi.io/golang:1.27-dev AS build
ARG TARGETARCH
WORKDIR /src
COPY helper/main.go helper/install.go helper/hook.go helper/transcript.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH="$TARGETARCH" \
      go build -trimpath -ldflags '-s -w' -o /out/usr/local/bin/sbx-agent-checkpoint \
      main.go install.go hook.go transcript.go \
 && go run main.go install.go hook.go transcript.go --version | grep -qx '0.1.0'

FROM scratch
COPY --from=build /out /

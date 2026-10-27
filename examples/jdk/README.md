# JDK

A mixin that adds a Java toolchain, Eclipse Temurin 25, to a workload that
ships none, such as `claude`. It copies the JDK tree out of the official
`eclipse-temurin` image into `/opt/java/openjdk` and sets `JAVA_HOME` and
`PATH`. Nothing lands in the agent's home.

The pattern works for any relocatable toolchain: a multi-stage build that
takes one directory from an upstream image, and a `FROM scratch` final stage
that ships only that directory. It does not work for apt packages. See
[RECIPES.md](../../skills/create-kit-v3/RECIPES.md).

```sh
sbx run docker.io/docker/sbx-kit-claude:2.1.311 --kit ./examples/jdk .
java -version    # inside the sandbox
```

Build against the current frontend source:

```sh
task kit:dev KIT=jdk
```

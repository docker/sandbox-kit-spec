# syntax=docker/dockerfile:1
# A JDK is a relocatable tree, so the overlay copies it out of the official
# image instead of installing a package: no dpkg state is left behind, and the
# overlay lands on any glibc base. The tag is the version authority; bump it
# together with `provides` in jdk.yaml.
FROM eclipse-temurin:25.0.4.1_1-jdk AS build
RUN /opt/java/openjdk/bin/java -version 2>&1 | grep -q '"25.0.4.1"' \
 || { echo "provides says jdk@25.0.4.1; the image ships something else" >&2; exit 1; }

# The overlay: one tree under /opt, nothing under /home/agent.
FROM scratch
COPY --from=build /opt/java/openjdk /opt/java/openjdk

# Static env on the final stage. PATH merges by appending, so a JDK the
# workload already ships keeps precedence over this one.
ENV JAVA_HOME=/opt/java/openjdk PATH=/opt/java/openjdk/bin

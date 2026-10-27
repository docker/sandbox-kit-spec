# Python

A mixin that adds CPython 3.14 and [uv](https://github.com/astral-sh/uv) to a
workload that ships no Python toolchain. The interpreter comes from
[python-build-standalone](https://github.com/astral-sh/python-build-standalone),
the relocatable builds `uv python install` uses. It lands in `/opt/python`, and
`uv` and `uvx` land in `/usr/local/bin`.

The official `python` image is not copied: its interpreter links that image's
own system libraries, which RECIPES.md says cannot travel in an overlay. A
standalone build carries its dependencies with it.

The kit allows `pypi.org` and `files.pythonhosted.org` at runtime so that
`uv` and `pip` can install packages.

```sh
sbx run docker.io/docker/sbx-kit-claude:2.1.311 --kit ./examples/python .
python3 --version && uv --version    # inside the sandbox
```

Build against the current frontend source:

```sh
task kit:dev KIT=python
```

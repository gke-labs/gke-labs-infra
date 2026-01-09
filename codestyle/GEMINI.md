This is codestyle, a go program to enforce code style requirements and guidelines.

We want to try to create a single tool that we can quickly run against PRs, agentic output, and run frequently in the development inner loop.
It is really important that codestyle be fast.

We also want to avoid having to reproduce the same boilerplate linter etc tooling in every git repo,
by creating the opinionated tooling here and encouraging others to use it.

We are inspired by gofmt; we don't want to support every style, we just want to enforce one style
quickly, so that if people are willing to adopt that style they fall into a "pit of success".

# Contributing to kubectl-status

First off, thanks for taking the time to contribute! ❤️

All types of contributions are encouraged and valued. See the [Table of Contents](#table-of-contents) for different ways
to help and details about how this project handles them. Please make sure to read the relevant section before making
your contribution. It will make it a lot easier for us maintainers and smooth out the experience for all involved. The
community looks forward to your contributions. 🎉

> And if you like the project, but just don't have time to contribute, that's fine. There are other easy ways to support the project and show your appreciation, which we would also be very happy about:
> - Star the project
> - Tweet about it
> - Refer this project in your project's readme
> - Mention the project at local meetups and tell your friends/colleagues

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [I Have a Question](#i-have-a-question)
- [I Want To Contribute](#i-want-to-contribute)
  - [Legal Notice](#legal-notice)
  - [Reporting Bugs](#reporting-bugs)
    - [Before Submitting a Bug Report](#before-submitting-a-bug-report)
    - [How Do I Submit a Good Bug Report?](#how-do-i-submit-a-good-bug-report-)
  - [Suggesting Enhancements](#suggesting-enhancements)
    - [Before Submitting an Enhancement](#before-submitting-an-enhancement)
    - [How Do I Submit a Good Enhancement Suggestion?](#how-do-i-submit-a-good-enhancement-suggestion-)
  - [Your First Code Contribution](#your-first-code-contribution)
  - [Claude Code Integration](#claude-code-integration)
  - [Improving The Documentation](#improving-the-documentation)
- [Styleguides](#styleguides)
  - [Commit Messages](#commit-messages)
- [Releasing a new version](#releasing-a-new-version)

## Code of Conduct

This project and everyone participating in it is governed by the
[kubectl-status Code of Conduct](https://github.com/bergerx/kubectl-status/blob/master/CODE_OF_CONDUCT.md). By
participating, you are expected to uphold this code. Please report unacceptable behavior to <>.

## I Have a Question

> If you want to ask a question, we assume that you have read the
> [README.md](https://github.com/bergerx/kubectl-status/blob/master/README.md) and [CONTRIBUTING.md](https://github.com/bergerx/kubectl-status/blob/master/CONTRIBUTING.md) (this file).

Before you ask a question, it is best to search for existing [Issues](https://github.com/bergerx/kubectl-status/issues)
that might help you. In case you have found a suitable issue and still need clarification, you can write your question
in this issue. It is also advisable to search the internet for answers first.

If you then still feel the need to ask a question and need clarification, we recommend the following:

- Open an [Issue](https://github.com/bergerx/kubectl-status/issues/new).
- Provide as much context as you can about what you're running into.

We will then take care of the issue as soon as possible.

## I Want To Contribute

> ### Legal Notice
> When contributing to this project, you must agree that you have authored 100% of the content,
> that you have the necessary rights to the content and that the content you contribute may be provided under
> the project license.

### Reporting Bugs

#### Before Submitting a Bug Report

A good bug report shouldn't leave others needing to chase you up for more information. Therefore, we ask you to
investigate carefully, collect information and describe the issue in detail in your report. Please complete the
following steps in advance to help us fix any potential bug as fast as possible.

- Make sure that you are using the latest version (`kubectl krew upgrade status`).
- Try running the faulty command with these extra flags as this would provide more details about what's going on behind
  the scenes:
    - `-v 3` reports warnings and ignored/silenced errors,
    - `-v 5` enables verbose logging
    - `-v 8` also logs Kubernetes all API requests and truncated responses, or
    - `-v 10` same with previous but doesn't truncate responses
- Determine if your bug is really a bug and not a missing component in your cluster (e.g. when metrics-server is not
  deployed, the node and pod outputs won't have usage details). If you are looking for support, you might want to
  check [this section](#i-have-a-question).
- To see if other users have experienced (and potentially already solved) the same issue you are having, check if there
  is not already a bug report existing for your bug or error in
  the [bug tracker](https://github.com/bergerx/kubectl-status/issues?q=label%3Abug).
- Also make sure to search the internet (including Stack Overflow) to see if users outside the GitHub community have
  discussed the issue.
- Familiarize with the [general guidelines](#general-guidelines).
- Collect information about the bug.
- Can you reliably reproduce the issue? And, can you also reproduce it with older versions?

#### How Do I Submit a Good Bug Report?

> You must never report security related issues, vulnerabilities or bugs to the issue tracker, or elsewhere in public.
> Instead sensitive bugs must be sent by email to <bekirdo at gmail.com>.
<!-- You may add a PGP key to allow the messages to be sent encrypted as well. -->

We use GitHub issues to track bugs and errors. If you run into an issue with the project:

- Open an [Issue](https://github.com/bergerx/kubectl-status/issues/new). (Since we can't be sure at this point whether
  it is a bug or not, we ask you not to talk about a bug yet and not to label the issue.)
- Explain the behavior you would expect and the actual behavior.
- Include these versions:
    - `kubectl status --version`
    - `kubectl version -o yaml`
    - `kubectl krew version` (only if you installed using krew)
- Try to include the output with the `-v 5` flag, also try to include the un-truncated response yamls
  (`kubectl get -o yaml ...`) for individual resources as they greatly help us to understand the issue.
  Please don't forget to mask any sensitive values.
- Please provide as much context as possible and describe the *reproduction steps* that someone else can follow to
  recreate the issue on their own. This usually includes your code. For good bug reports you should isolate the problem
  and create a reduced test case.
- Provide the information you collected in the previous section.

Once it's filed:

- The project team will label the issue accordingly.
- A team member will try to reproduce the issue with your provided steps. If there are no reproduction steps or no
  obvious way to reproduce the issue, the team will ask you for those steps and mark the issue as `needs-repro`. Bugs
  with the `needs-repro` tag will not be addressed until they are reproduced.
- If the team is able to reproduce the issue, it will be marked `needs-fix`, as well as possibly other tags (such
  as `critical`), and the issue will be left to be [implemented by someone](#your-first-code-contribution).

<!-- You might want to create an issue template for bugs and errors that can be used as a guide and that defines the
     structure of the information to be included. If you do so, reference it here in the description. -->
### Suggesting Enhancements

This section guides you through submitting an enhancement suggestion for kubectl-status, **including completely new
features and minor improvements to existing functionality**. Following these guidelines will help maintainers and the
community to understand your suggestion and find related suggestions.

#### Before Submitting an Enhancement

- Make sure that you are using the latest version.
- Familiarize with the [general guidelines](#general-guidelines).
- Read the [documentation](https://github.com/bergerx/kubectl-status/blob/master/README.md) carefully and find out if
  the functionality is already covered, maybe by an individual configuration.
- Perform a [search](https://github.com/bergerx/kubectl-status/issues) to see if the enhancement has already been
  suggested. If it has, add a comment to the existing issue instead of opening a new one.
- Find out whether your idea fits with the scope and aims of the project. It's up to you to make a strong case to
  convince the project's developers of the merits of this feature. Keep in mind that we want features that will be
  useful to the majority of our users and not just a small subset. If you're just targeting a minority of users,
  consider writing an add-on/plugin library.

#### How Do I Submit a Good Enhancement Suggestion?

Enhancement suggestions are tracked as [GitHub issues](https://github.com/bergerx/kubectl-status/issues).

- Use a **clear and descriptive title** for the issue to identify the suggestion.
- Provide a **step-by-step description of the suggested enhancement** in as many details as possible.
- **Describe the current behavior** and **explain which behavior you expected to see instead** and why. At this point
  you can also tell which alternatives do not work for you.
- You may want to **include screenshots** which help you demonstrate the steps or point out the part which the
  suggestion is related to.
- **Explain why this enhancement would be useful** to most kubectl-status users. You may also want to point out the
  other projects that solved it better and which could serve as inspiration.

### Design conventions

Before writing or reviewing any template output, read [**CONVENTIONS.md**](CONVENTIONS.md). It covers output philosophy, color coding, template section order, prose style, value highlighting, and the shallow/default/deep rendering pattern.

### Your First Code Contribution

Then use `make` to get the compiled binary:

```bash
make
# the binary will be linked into the bin/ folder
bin/status pods
```

When working on a specific object, it may be easier to save the object and work on it locally:

```bash
kubectl get pod test-pod -o yaml > test-pod.yaml
# make changes on the output
make
bin/status --local -f test-pod.yaml
```

Before submitting a PR, ensure tests pass:

```bash
make test
```

### Claude Code Integration

The project ships a [Claude Code](https://claude.ai/code) skill and project-level settings under `.claude/`.

**`/generate-template`** — generates a kubectl-status Go template for any CRD present in your current kubectl context. Run it in Claude Code and provide the resource kind; the skill reads the CRD schema, samples live instances, and writes a ready-to-use template to `~/.kubectl-status/templates/<Kind>.tmpl` following all output and color-coding guidelines in this file.

```bash
# Inside Claude Code — example invocation:
/generate-template HTTPRoute
```

### Working with Test Artifacts

Test artifacts in `tests/artifacts/` verify template output changes. When modifying templates:

1. **Regenerate outputs** after template changes:
   ```bash
   make update-artifacts
   ```

2. **Add new test cases** when adding support for new resource types:
   ```bash
   make new-artifact CMD='-n default pod/my-pod' FILE='pod-example'
   make test
   ```

3. **Include updated artifacts in PRs** - reviewers use `.out` file diffs to verify template changes.

### Running e2e Tests Locally

`make test-e2e` runs the `TestE2E*` suite against a real cluster (see `cmd/main_test.go`). By
default it starts its own minikube cluster; set `ASSUME_MINIKUBE_IS_CONFIGURED=true` if your
current kubeconfig context already points at a suitable cluster (this is what CI does).

Some e2e scenarios exercise cert-manager-issued TLS `Secret`s and Gateway API objects.
`make test-e2e` installs both automatically (via its `install-e2e-deps` prerequisite target
in the `Makefile`) against whatever cluster is configured before running the test suite — no
separate manual setup needed. Bump the pinned versions in that target periodically to track
upstream stable releases; CI uses the same `make test-e2e` target, so it stays in sync
automatically.

When a template change adds or touches `$.KubeGetFirst`, `$.IncludeRenderableObject`/`$.Include`,
or any other interaction with a live cluster, add or extend a case in `TestE2EDynamicManifests`
(`cmd/main_test.go`) plus matching manifests/regex fixtures under `tests/e2e-artifacts/`. The
offline golden-file tests (`TestAllArtifactsLocal*`) run with `--shallow` (alongside `--local`,
since there's no live cluster to query either way), which makes `KubeGetFirst` a no-op — they
can't exercise the "found the related object" or `--deep` include branches, so the live e2e suite
is the only place that covers them.

### Improving The Documentation

We don't yet have a comprehensive documentation, we maintain just a few Markdown files in the repo. We aim to keep the
examples in the [README.md](README.md#demo) up-to-date as we add new features, but this process is not automated.

## Styleguides

### Commit Messages

We don't yet have a convention for commit messages.

## Releasing a new version

Pushing a git tag will trigger
[goreleaser GitHub action](https://github.com/bergerx/kubectl-status/actions/workflows/release.yml)
to build and publish a new release to krew index.

```bash
git tag vX.X.X
git push --tags
```

## Attribution

This guide is based on the **contributing-gen**. [Make your own](https://github.com/bttger/contributing-gen)!

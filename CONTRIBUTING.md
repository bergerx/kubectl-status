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
  - [General Guidelines](#general-guidelines)
    - [Output Contents Guidelines](#output-contents-guidelines)
    - [Color Coding Guidelines](#color-coding-guidelines)
  - [Your First Code Contribution](#your-first-code-contribution)
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

### General Guidelines

#### Output Contents Guidelines

* Aim output to be for humans "only", don't put any effort to make the output friendly for parsers.
* Try to keep the output as compact as possible. Compact output is one of the main differentiation points
  from `kubectl describe`.
* It's tempting to assume colors always work but don't forget that users will want to share the output with others by
  copy-paste, which results in lost ASCII color codes. E.g., coloring "Ready" in green or red to indicate the status is
  usually not ideal. Prefer "Not Ready" for representing the faulty state in such cases.
* Not all status fields/values are meaningful as they are represented in the raw Kubernetes resources, don't try to keep
  them as they are if you can make it more human-friendly. E.g., Prefer "Not Ready" over "Ready: false".
* Drop not so meaningful fields or fields with a well-known default from the output. E.g., podIP, hostIP, containerID,
  imageID of a pod doesn't hold much value for understanding the status of a pod.
* Be opinionated about representing the status, as long as it helps users get the current status of the resource in
  question.
* Assume some level of knowledge and don't try to over-explain, but explain non-obvious/edge cases and be explicit about
  possibly impacting states of resources. E.g., ongoing rollout issues, or not having any "Ready" Pods in a Deployment (
  usually means Outage), or a Service with no endpoints (again likely an Outage).
* Don't include spec fields unless they have significant value for setting the context for the current status. E.g.,
  Knowing the .spec.replicas value is relevant to understand the status of a ReplicaSet, but ingress host values are
  pure spec.
* When some related information is not available in the status fields of a resource, go the extra mile of doing further
  queries to obtain more information which may be helpful for users to understand the current status. E.g., fetch
  NodeMetrics and Pods when showing a node's status.
* Being aligned with the terminology used by Kubernetes and used in the individual status fields is good but not
  mandatory.
* If you can identify conventions in the status fields, make them generic templates and include them in the
  DefaultResource template, so any other CRDs following those conventions can benefit right away without having to
  implement a new Kind template. E.g., `observedGeneration`, `conditions`, `replicas`.

#### Color Coding Guidelines

* Follow traffic lights convention. Users expect them to map to error/warning/ok consecutively. But, prefer \[`red`
  /`yellow`/regular] over \[`red`/`yellow`/`green`] to prevent `green` over-usage.
* Don't use `green` extensively. Use when a dedicated status field indicates an explicit healthy status. E.g., use green
  when Ready is True (or "active", "running"), but don't use when ready replicas are matching the desired replicas.
* Use `yellow` for issues that are well known to be transient but may be impacting or bad practices. E.g., ongoing
  rollout, or using a `latest` image tag.
* Use `bold red` for potential issues that need attention.
* Prefer `red` over `bold red` for long explanation/description of a faulty state. E.g.
  for `.status.conditions[].message` field for a faulty condition.
* Prefer `bold red` over `red` if the highlighted text is a single word, camelCase, or PascalCase. E.g. for `
  .status.conditions[].reason field for a faulty condition.
* When you need to colorize a short key/value pair in a faulty state, prefer highlighting both the key and the value,
  not either. E.g., For `readyPodCount:0`, paint the whole expression to `red` rather than just key or value.

<!-- You might want to create an issue template for enhancement suggestions that can be used as a guide and that defines
     the structure of the information to be included. If you do so, reference it here in the description. -->
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

kubectl-status follows the below guidelines to have a consistent user experience across different resources.

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

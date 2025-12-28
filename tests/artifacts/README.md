# Test artifacts

This folder holds various example yaml files and their rendered outputs (without color).
We use these to track/verify the impact of the changes we introduce to the templates.
Every time we change a template, it's expected to update corresponding out files here.

## Re-generate all the "*.out" files

```bash
make update-artifacts
```

## Adding a new case

```bash
make new-artifact CMD='-n default node,service' FILE='node-and-service'
make test
git add tests/artifacts/<file>.yaml tests/artifacts/<file>.out
```

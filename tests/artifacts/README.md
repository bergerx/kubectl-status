# Test artifacts

This folder holds various example yaml files and their rendered outputs (without color).
We use these to track/verify the impact of the changes we introduce to the templates.
Every time we change a template, it's expected to update corresponding out files here.

# Re-generate all the "*.out" files

```bash
cd ../..
for yaml in ./tests/artifacts/*.yaml; do
  out=$(echo ${yaml} | sed 's/.yaml/.out/')
  echo "${yaml} --> ${out}"
  go run ./cmd --time-hack-ago -f ${yaml} --local --shallow > ${out}
done
```

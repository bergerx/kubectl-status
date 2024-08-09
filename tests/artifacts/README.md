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

# Adding a new case

First generate the artifact file using a command like this:

```bash
cmd=""  # the command line parameters for generating the new manifest, e.g.: -n default node,service
file=""  # filename for the new artifact file to be stored, e.g. node-and-service

cd ../..
kubectl get -o yaml ${cmd} > tests/artifacts/${file}.yaml
go run ./cmd --time-hack-ago ${cmd} --shallow > tests/artifacts/${file}.out
make test
git add tests/artifacts/${file}.yaml tests/artifacts/${file}.out
```

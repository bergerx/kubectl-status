
TODO: `wait-for` comments are expected in manifest files
colors are ignored
generated content is kept in the `./generated` folder
you can check `./generated/*.raw_yamls` for debugging but they are not git-ignored. They are only to help debugging
one namespace for each test will be created
tests run in parallel
anything mathing `"\b[0-9]\+s\b"` regex will be assumed a duration and replaced with `"Xs"` in the generated files


## Coding

* never build code in test, instead, put build commands/scripting in a Makefile and use that	

* when writing tests, never use sleep unless absolutely necessary, instead, prefer explicit signaling/checking for readiness

* when creating protocols, prefer request/response type messages.  the response message should have an error boolean and a message field that's filled out with an error message, ideally, with
some context, though only what's readily accessible

* for debugging purposes, use the language's typical logging framework to log major events to stderr. include context where appropriate such as requestid on every line related to a particular request.
  * one natural point to log is every time a message is sent/received
  * use the 'debug' level so these messages aren't typically printed

* go parses the whole file before resolving symbols, so structure go files such that the most important code is before less important code.

* prefer writing end to end (e2e) tests over writing unit tests, but unit tests are still good for particular tricky code and/or code that is hard to test using an end to end test

* don't write tests that just exercise the standard library (e.g., JSON marshal/unmarshal round-trips).  tests should validate project-specific behavior.

* don't write redundant tests that cover the same code path from different angles.  one well-placed e2e or integration test that exercises the full pipeline is better than multiple unit tests poking at the same intermediate step.  unit tests should focus on edge cases and error paths that e2e tests can't easily reach (malformed input, boundary conditions).

* consolidate duplicated code before committing, not as a follow-up.  if the same logic exists in two places, extract it immediately.

* when inventing wire formats or encodings, check whether an existing standard covers the use case before designing something ad-hoc.

* do not commit without allowing me to review.

* in general, don't comment code to describe what it's doing unless it's tricky or hard to understand.  better to have descriptive naming and structure.  save comments for things that the code doesn't convey, like intent, purpose, or design decisions that are not obvious.

* anytime that flake.nix is changed, we need to stop our session so that the claude human user can reload the new development environment.


## Environment

* you're running on nixos-25.11.  nix works a bit differently than most linux distributions, the most notable is that binaries do not live in /bin or /usr/bin.  do not reference commands directly by their path ever.

* you're also running inside a bubblewrap'd sandbox which only has write access to this directory, it's children, /tmp, and /dev.  if there are access problems (i.e. some tool you run needs to access a file outside the sandbox), stop and ask to resolve.

* if a shell command you run fails, stop and ask me if we should install it, or if you shouldn't use it any longer.


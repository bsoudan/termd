{
  description = "termd — terminal multiplexer with proper separation of concerns";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        devShells.default = pkgs.mkShell {
          name = "termd-dev";

          packages = with pkgs; [
            bash
            gnumake

            # Server + Frontend + termctl (Go)
            go
            gopls
            gotools  # goimports, etc.
            gcc      # C/C++ compiler for cgo

            # Protocol debugging: NDJSON over a Unix socket
            jq
            netcat-gnu  # nc for manual socket interaction

            # Version control
            git

            # Sandbox
            bubblewrap

          ];

          shellHook = ''
            export CLAUDE_CONFIG_DIR=$PWD/.claude-config
            export PATH="$PWD/.local/bin:$PATH"
            export GOPATH="$PWD/.local/go"
            export GOCACHE="$PWD/.local/var/cache/go"
            export NIX_SHELL=termd

            echo
            echo "entering termd dev environment"
            echo "  * go  $(go version)"
            echo "  * gcc $(gcc --version | awk 'NR==1{print $NF}')"
            if [ -z "$IN_SANDBOXED_SHELL" ]; then
              export IN_SANDBOXED_SHELL=1
              exec bwrap \
                --ro-bind / / \
                --bind "$PWD" "$PWD" \
                --bind "/tmp" "/tmp" \
                --dev-bind /dev /dev \
                --proc /proc \
                --die-with-parent \
                --setenv IN_SANDBOXED_SHELL 1 \
                -- bash -i
            fi
          '';

        };
      }
    );
}

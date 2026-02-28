{
  description = "Prometheus/OTEL exporter for Claude.ai session and weekly usage metrics";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        claude-usage-exporter = pkgs.buildGoModule {
          pname = "claude-usage-exporter";
          version = "0.1.4";
          src = ./.;
          # Run `nix build` once to get the correct hash from the error message.
          vendorHash = "sha256-9K619hTXYJ0h1c9ZEZLHNPwJxVRDTW0XFFMD55nGezk=";
          env.CGO_ENABLED = "0";
          ldflags = [ "-s" "-w" ];
          meta.mainProgram = "claude-usage-exporter";
        };
      in
      {
        packages = {
          default = claude-usage-exporter;

          dockerImage = pkgs.dockerTools.buildLayeredImage {
            name = "ghcr.io/graemef/claude-usage-exporter";
            tag = "latest";
            # cacert is required for HTTPS calls to claude.ai
            contents = [ claude-usage-exporter pkgs.cacert ];
            config = {
              Entrypoint = [ (pkgs.lib.getExe claude-usage-exporter) ];
              ExposedPorts."9091/tcp" = {};
              Env = [
                "ACCOUNTS_FILE=/config/accounts.yaml"
                "LISTEN_ADDR=:9091"
              ];
            };
          };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gopls gotools ];
        };
      });
}

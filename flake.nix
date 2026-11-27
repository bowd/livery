{
  description = "Livery (liv) — declarative multi-host AI agent configuration & profile launcher";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "livery";
          version = "0.1.0";
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/liv" ];
          env.CGO_ENABLED = "0";
          ldflags = [
            "-s"
            "-w"
            "-X github.com/bowd/livery/internal/version.Version=0.1.0"
          ];
          meta = with pkgs.lib; {
            description = "Declarative multi-host AI agent configuration & profile launcher (liv)";
            mainProgram = "liv";
            platforms = platforms.unix;
          };
        };
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.system}.default}/bin/liv";
        };
      });
    };
}

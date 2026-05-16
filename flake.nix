{
  description = "methlab";

  nixConfig = {
    extra-substituters = [
      "https://shit.cuddles.rs/nixos"
    ];
    extra-trusted-public-keys = [
      "shit.cuddles.rs:HQ4GqwV3aPbneoDdl4diqMcRjmusLqtQkETdebH62sk="
    ];
  };

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};

      methlab = system: (pkgsFor system).buildGoModule {
        pname = "methlab";
        version = "0.1.0";
        src = ./.;
        vendorHash = "sha256-JaYPom4yGC3TObxXZ4jpnewXmLzNaWReQfMhoFkYFiQ=";
      };
    in {
      packages = forAllSystems (system: {
        default = methlab system;
      });

      devShells = forAllSystems (system: {
        default = (pkgsFor system).mkShell {
          buildInputs = with (pkgsFor system); [ go gopls gotools ];
        };
      });

      hydraJobs = forAllSystems (system: {
        methlab = methlab system;
      });
    };
}
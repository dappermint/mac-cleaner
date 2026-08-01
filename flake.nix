{
  description = "Whole-surface macOS storage accounting and cleanup TUI";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      pkgsFor = system: nixpkgs.legacyPackages.${system};
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = self.packages.${system}.mac-cleaner;
          mac-cleaner = pkgs.callPackage ./package.nix { };
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              gotools
              golangci-lint
              just
              nixfmt
              deadnix
              statix
            ];
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);

      checks = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          build = self.packages.${system}.mac-cleaner;

          lint =
            pkgs.runCommand "lint"
              {
                nativeBuildInputs = [
                  pkgs.deadnix
                  pkgs.statix
                  pkgs.nixfmt
                ];
              }
              ''
                cd ${self}
                deadnix --fail .
                statix check .
                nixfmt --check flake.nix package.nix
                touch $out
              '';
        }
      );
    };
}

{
  description = "Triage Factory — AI-powered issue triage";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        src = pkgs.lib.cleanSourceWith {
          src = ./.;
          filter =
            path: type:
            let
              baseName = baseNameOf path;
              relPath = pkgs.lib.removePrefix (toString ./. + "/") (toString path);
            in
            baseName != ".git"
            && baseName != ".claude"
            && baseName != "node_modules"
            && baseName != "dist"
            && baseName != "flake.nix"
            && baseName != "flake.lock"
            && !(pkgs.lib.hasPrefix "frontend/node_modules" relPath)
            && !(pkgs.lib.hasPrefix "frontend/dist" relPath);
        };

        frontend = pkgs.buildNpmPackage {
          pname = "triage-factory-frontend";
          version = "0.0.0";
          src = "${src}/frontend";
          npmDepsHash = "sha256-Jl+3dIOQDsm+ZTze3Rv8+udGPCInQTQmMm6hZGAur+0=";
          NODE_OPTIONS = "--max-old-space-size=4096";
          installPhase = ''
            runHook preInstall
            mkdir -p $out
            cp -r dist/* $out/
            runHook postInstall
          '';
        };
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "triagefactory";
          version = (builtins.fromJSON (builtins.readFile ./.github/release-please-manifest.json)).".";
          inherit src;
          vendorHash = "sha256-lYYe5UaP1ohDK8hXLZFIpxksa9liVr76Yt0xOjwt884=";

          nativeCheckInputs = [ pkgs.git ];

          checkFlags =
            let
              skippedTests = [
                "TestImport_RoundTripSessionTreeAndCompactions"
              ];
            in
            [ "-run" "^(?!${builtins.concatStringsSep "|" skippedTests}$)" ];

          preCheck = ''
            export HOME=$(mktemp -d)
            git config --global user.email "test@test.com"
            git config --global user.name "Test"
          '';

          preBuild = ''
            mkdir -p frontend/dist
            cp -r ${frontend}/* frontend/dist/
          '';

          postInstall = ''
            mv $out/bin/triage-factory $out/bin/triagefactory
          '';

          meta = {
            description = "AI-powered issue triage";
            mainProgram = "triagefactory";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            nodejs
            gopls
            gotools
          ];
        };
      }
    );
}

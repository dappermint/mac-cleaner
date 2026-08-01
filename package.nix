{
  lib,
  buildGoModule,
  version ? "0.1.0",
}:

buildGoModule {
  pname = "mac-cleaner";
  inherit version;

  src = ./.;
  vendorHash = null;

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  postInstall = ''
    install -Dm644 README.md "$out/share/doc/mac-cleaner/README.md"
  '';

  meta = {
    description = "Whole-surface macOS storage accounting and cleanup TUI";
    mainProgram = "mac-cleaner";
    platforms = lib.platforms.darwin;
  };
}

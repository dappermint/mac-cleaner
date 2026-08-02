{
  lib,
  buildGoModule,
  version ? "0.88.0",
}:

buildGoModule {
  pname = "mac-cleaner";
  inherit version;

  __structuredAttrs = true;

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
    homepage = "https://github.com/dappermint/mac-cleaner";
    license = lib.licenses.gpl3Only;
    mainProgram = "mac-cleaner";
    platforms = lib.platforms.darwin;
  };
}

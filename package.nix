{
  lib,
  buildGoModule,
  version ? "0.90.0",
}:

buildGoModule {
  pname = "ratatouille";
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
    install -Dm644 README.md "$out/share/doc/ratatouille/README.md"
    ln -s ratatouille "$out/bin/rat"
  '';

  meta = {
    description = "Whole-surface macOS storage accounting and cleanup TUI";
    homepage = "https://github.com/dappermint/ratatouille";
    license = lib.licenses.gpl3Only;
    mainProgram = "ratatouille";
    platforms = lib.platforms.darwin;
  };
}

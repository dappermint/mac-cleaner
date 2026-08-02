package cli

// The completions are handwritten rather than generated, because the command
// surface is small and a generator would pull in a dependency to save a page.

const fishCompletion = `# ratatouille fish completion
# install: ratatouille completion fish > ~/.config/fish/completions/ratatouille.fish

set -l commands scan surface plan clean uninstall purge installer status history config completion touchid update version help

complete -c ratatouille -f
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a scan -d "inventory, machine or human readable"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a surface -d "account for every byte, or explore one path"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a plan -d "what a selection would do"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a clean -d "run selected cleanup actions"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a uninstall -d "remove apps and their leftovers"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a purge -d "remove project build artifacts"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a installer -d "remove installer downloads"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a status -d "live system metrics"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a history -d "what this tool did"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a config -d "whitelists and search paths"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a completion -d "shell completion script"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a touchid -d "Touch ID for sudo"
complete -c ratatouille -n "not __fish_seen_subcommand_from $commands" -a update -d "how to upgrade this install"

complete -c ratatouille -l root -d "uid 0, adds System Data and other users"
complete -c ratatouille -l debug -d "write guard and timing diagnostics to stderr"
complete -c ratatouille -l json -d "machine-readable output"
complete -c ratatouille -l dry-run -d "change nothing"
complete -c ratatouille -n "__fish_seen_subcommand_from surface" -l files -d "list the largest files"
complete -c ratatouille -n "__fish_seen_subcommand_from surface" -l depth -d "levels to print"
complete -c ratatouille -n "__fish_seen_subcommand_from surface" -l min-size -d "large file floor"
complete -c ratatouille -n "__fish_seen_subcommand_from surface" -a "(__fish_complete_directories)"
complete -c ratatouille -n "__fish_seen_subcommand_from clean plan" -l all-safe -d "select every safe action"
complete -c ratatouille -n "__fish_seen_subcommand_from clean" -l external -d "clean a directly mounted external volume"
complete -c ratatouille -n "__fish_seen_subcommand_from uninstall" -l list -d "installed apps and their exact names"
complete -c ratatouille -n "__fish_seen_subcommand_from uninstall" -l permanent -d "bypass Trash"
complete -c ratatouille -n "__fish_seen_subcommand_from status" -l watch -d "keep sampling"
complete -c ratatouille -n "__fish_seen_subcommand_from status" -l explain -d "show the score components"
complete -c ratatouille -n "__fish_seen_subcommand_from history" -l since -d "only newer than, e.g. 7d"
complete -c ratatouille -n "__fish_seen_subcommand_from config" -a "show path whitelist optimize-whitelist purge-paths"
complete -c ratatouille -n "__fish_seen_subcommand_from completion" -a "fish zsh bash"
complete -c ratatouille -n "__fish_seen_subcommand_from touchid" -a "status enable disable"

complete -c rat -w ratatouille
`

const zshCompletion = `#compdef ratatouille rat
# install: ratatouille completion zsh > "${fpath[1]}/_ratatouille"

_ratatouille() {
  local -a commands
  commands=(
    'scan:inventory, machine or human readable'
    'surface:account for every byte, or explore one path'
    'plan:what a selection would do'
    'clean:run selected cleanup actions'
    'uninstall:remove apps and their leftovers'
    'purge:remove project build artifacts'
    'installer:remove installer downloads'
    'status:live system metrics'
    'history:what this tool did'
    'config:whitelists and search paths'
    'completion:shell completion script'
    'touchid:Touch ID for sudo'
    'update:how to upgrade this install'
    'version:print the version'
    'help:print usage'
  )

  _arguments -C \
    '--root[uid 0, adds System Data and other users]' \
    '--debug[write guard and timing diagnostics to stderr]' \
    '--json[machine-readable output]' \
    '1: :->command' \
    '*:: :->argument'

  case $state in
    command) _describe 'command' commands ;;
    argument)
      case $words[1] in
        surface) _arguments '--files[list the largest files]' '--depth[levels to print]:depth:' \
                            '--min-size[large file floor]:size:' '--verify[live filesystem check]' '*:directory:_files -/' ;;
		clean) _arguments '--all-safe[select every safe action]' '--dry-run[change nothing]' '--external[clean external volume]:volume:_files -/' ;;
		plan) _arguments '--all-safe[select every safe action]' '--dry-run[change nothing]' ;;
        uninstall) _arguments '--list[installed apps and their exact names]' '--permanent[bypass Trash]' '--dry-run[change nothing]' ;;
        purge) _arguments '--all[include recently touched projects]' '--trash[route to Trash]' '--dry-run[change nothing]' ;;
        installer) _arguments '--min-size[size floor]:size:' '--dry-run[change nothing]' ;;
        status) _arguments '--watch[keep sampling]' '--explain[show the score components]' '--interval[between samples]:duration:' ;;
        history) _arguments '--since[only newer than]:duration:' '--limit[how many]:count:' ;;
        config) _values 'file' show path whitelist optimize-whitelist purge-paths ;;
        completion) _values 'shell' fish zsh bash ;;
        touchid) _values 'action' status enable disable ;;
      esac
      ;;
  esac
}

_ratatouille "$@"
`

const bashCompletion = `# ratatouille bash completion
# install: ratatouille completion bash > /usr/local/etc/bash_completion.d/ratatouille

_ratatouille() {
  local current previous commands
  current="${COMP_WORDS[COMP_CWORD]}"
  previous="${COMP_WORDS[COMP_CWORD-1]}"
  commands="scan surface plan clean uninstall purge installer status history config completion touchid update version help"

  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "$commands" -- "$current") )
    return
  fi

  case "${COMP_WORDS[1]}" in
    surface)   COMPREPLY=( $(compgen -W "--files --depth --min-size --limit --verify --json --root" -- "$current") ) ;;
    clean)     COMPREPLY=( $(compgen -W "--all-safe --external --dry-run --deep --yes --root --debug" -- "$current") ) ;;
    plan)      COMPREPLY=( $(compgen -W "--all-safe --deep --root" -- "$current") ) ;;
    uninstall) COMPREPLY=( $(compgen -W "--list --dry-run --permanent --leftovers-only --json" -- "$current") ) ;;
    purge)     COMPREPLY=( $(compgen -W "--all --trash --dry-run --min-age --json --yes" -- "$current") ) ;;
    installer) COMPREPLY=( $(compgen -W "--min-size --dry-run --json --yes" -- "$current") ) ;;
    status)    COMPREPLY=( $(compgen -W "--watch --interval --explain --json" -- "$current") ) ;;
    history)   COMPREPLY=( $(compgen -W "--since --limit --id --json" -- "$current") ) ;;
    config)    COMPREPLY=( $(compgen -W "show path whitelist optimize-whitelist purge-paths" -- "$current") ) ;;
    completion) COMPREPLY=( $(compgen -W "fish zsh bash" -- "$current") ) ;;
    touchid)   COMPREPLY=( $(compgen -W "status enable disable" -- "$current") ) ;;
    *)         COMPREPLY=( $(compgen -W "--json --root --debug" -- "$current") ) ;;
  esac
}

complete -F _ratatouille ratatouille
complete -F _ratatouille rat
`

#compdef db-query
compdef _db-query db-query

# zsh completion for db-query.
#
# Install (either route works):
#   source <(db-query completion zsh)                  # add to ~/.zshrc
#   db-query completion zsh > "${fpath[1]}/_db-query"  # then run: compinit
#
# Dynamic values (hosts, saved queries, categories) are fetched at completion
# time from the hidden `db-query __complete` command, which reads only local
# config and saved-query files — never a credential or a database.

# __dbq_complete <target> asks the binary for candidates and adds them with
# their descriptions. The helper prints "name<TAB>description" lines; splitting
# on the literal tab keeps colons and spaces in a SQL preview from corrupting
# the candidate list. An already-typed --config/--category is passed through so
# completion reflects the configuration actually in effect.
__dbq_complete() {
  local target=$1
  local -a values displays ctx
  local name desc
  [[ -n ${opt_args[--config]} ]]   && ctx+=(--config "${opt_args[--config]}")
  [[ -n ${opt_args[--category]} ]] && ctx+=(--category "${opt_args[--category]}")
  db-query __complete "${ctx[@]}" "$target" 2>/dev/null | while IFS=$'\t' read -r name desc; do
    values+=("$name")
    displays+=("${name}  --  ${desc}")
  done
  (( ${#values} )) && compadd -d displays -a values
}
__dbq_hosts()      { __dbq_complete host }
__dbq_sources()    { __dbq_complete source }
__dbq_categories() { __dbq_complete category }

_db-query() {
  local curcontext="$curcontext" state line
  typeset -A opt_args

  _arguments -C \
    '1: :->command' \
    '*:: :->args' && return

  case $state in
    command)
      local -a commands
      commands=(
        'query:run ad-hoc SQL or a saved query'
        'queries:list saved queries'
        'introspect:list tables and columns, rebuild the schema cache'
        'hosts:list configured hosts'
        'version:print version information'
        'completion:print the zsh completion script'
        'help:show usage'
      )
      _describe -t commands 'db-query command' commands
      ;;
    args)
      case $line[1] in
        query)      _dbq_cmd_query ;;
        queries)    _dbq_cmd_queries ;;
        introspect) _dbq_cmd_introspect ;;
        hosts)      _dbq_cmd_hosts ;;
        completion) _dbq_cmd_completion ;;
      esac
      ;;
  esac
}

_dbq_cmd_query() {
  _arguments \
    '--host[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '--config[config file path]:file:_files' \
    '--output[output format]:format:(text json)' \
    '*--param[bind a query parameter, k=v]:param:' \
    '-f[read SQL from file ("-" for stdin)]:file:_files' \
    '--save[save the query under this name after it succeeds]:name:' \
    '--source[run a saved query by name]:name:__dbq_sources' \
    '--category[saved-query category]:category:__dbq_categories' \
    '--force[overwrite an existing saved query]' \
    '--timeout[per-invocation deadline]:duration:' \
    '--refresh-schema[rebuild the schema cache first]' \
    '--no-headers[text output: omit the header line]' \
    '*:SQL:_message "SQL is free-form; no completion"'
}

_dbq_cmd_queries() {
  _arguments \
    '--config[config file path]:file:_files' \
    '--output[output format]:format:(text json)' \
    '--category[restrict to one saved-query category]:category:__dbq_categories'
}

_dbq_cmd_introspect() {
  _arguments \
    '--host[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '--config[config file path]:file:_files' \
    '--output[output format]:format:(text json)' \
    '--timeout[per-invocation deadline]:duration:' \
    '--refresh-schema[rebuild the schema cache]'
}

_dbq_cmd_hosts() {
  _arguments \
    '--config[config file path]:file:_files' \
    '--output[output format]:format:(text json)'
}

_dbq_cmd_completion() {
  _arguments '1:shell:(zsh)'
}

# When sourced or eval-ed (rather than autoloaded off $fpath), the #compdef tag
# above is inert; the compdef call near the top registers the function instead.
# This guard keeps the function from running at source time.
if [ "${funcstack[1]}" = "_db-query" ]; then
  _db-query "$@"
fi

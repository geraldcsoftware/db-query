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
  [[ -n ${opt_args[-c]} ]]         && ctx+=(--config "${opt_args[-c]}")
  [[ -n ${opt_args[--category]} ]] && ctx+=(--category "${opt_args[--category]}")
  [[ -n ${opt_args[-C]} ]]         && ctx+=(--category "${opt_args[-C]}")
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

  # The shared flags are also accepted before the command, so they are offered
  # at the top level too — which is what makes `db-query --host <TAB>` complete
  # hosts before a command has been typed. A flag matched here lands in the
  # same opt_args map the per-command functions write to, so a global --config
  # is passed through to the dynamic helpers as well.
  _arguments -C \
    '(-H --host)'{-H,--host}'[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)' \
    '(-t --timeout)'{-t,--timeout}'[per-invocation deadline]:duration:' \
    '1: :->command' \
    '*:: :->args' && return

  case $state in
    command)
      # Not named `aliases`: that is a zsh special parameter (the alias table),
      # and shadowing it with a plain array is an assignment-type error.
      local -a commands shorthands
      commands=(
        'query:run ad-hoc SQL or a saved query'
        'list:list saved queries'
        'schema:show the cached schema, a table, or the table list'
        'introspect:list tables and columns, rebuild the schema cache'
        'hosts:list configured hosts'
        'version:print version information'
        'completion:print the zsh completion script'
        'help:show usage'
      )
      # Shorthands are their own group so they never crowd out the full names.
      shorthands=(
        'q:shorthand for query'
        's:shorthand for schema'
        'i:shorthand for introspect'
        'ls:shorthand for list'
        'l:shorthand for list'
      )
      _describe -t commands 'db-query command' commands
      _describe -t shorthands 'db-query shorthand' shorthands
      ;;
    args)
      case $line[1] in
        query|q)      _dbq_cmd_query ;;
        list|ls|l)    _dbq_cmd_list ;;
        schema|s)     _dbq_cmd_schema ;;
        introspect|i) _dbq_cmd_introspect ;;
        hosts)        _dbq_cmd_hosts ;;
        completion)   _dbq_cmd_completion ;;
      esac
      ;;
  esac
}

_dbq_cmd_query() {
  _arguments \
    '(-H --host)'{-H,--host}'[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)' \
    '*'{-p,--param}'[bind a query parameter, k=v]:param:' \
    '(-f --file)'{-f,--file}'[read SQL from file ("-" for stdin)]:file:_files' \
    '--save[save the query under this name after it succeeds]:name:' \
    '(-s --source)'{-s,--source}'[run a saved query by name]:name:__dbq_sources' \
    '(-C --category)'{-C,--category}'[saved-query category]:category:__dbq_categories' \
    '--force[overwrite an existing saved query]' \
    '(-t --timeout)'{-t,--timeout}'[per-invocation deadline]:duration:' \
    '--refresh-schema[rebuild the schema cache first]' \
    '--no-headers[text output: omit the header line]' \
    '*:SQL:_message "SQL is free-form; no completion"'
}

_dbq_cmd_list() {
  _arguments \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)' \
    '(-C --category)'{-C,--category}'[restrict to one saved-query category]:category:__dbq_categories'
}

_dbq_cmd_schema() {
  _arguments \
    '(-H --host)'{-H,--host}'[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)' \
    '(-t --timeout)'{-t,--timeout}'[per-invocation deadline]:duration:' \
    '--refresh-schema[rebuild the schema cache first]' \
    '--no-headers[text output: omit the header line]' \
    '(-T --tables)'{-T,--tables}'[print one schema-qualified table name per line]' \
    '*:table:_message "table name (bare or schema-qualified) from the cached schema"'
}

_dbq_cmd_introspect() {
  _arguments \
    '(-H --host)'{-H,--host}'[host entry from config]:host:__dbq_hosts' \
    '(-d --database)'{-d,--database}'[override the host database]:database:' \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)' \
    '(-t --timeout)'{-t,--timeout}'[per-invocation deadline]:duration:' \
    '--refresh-schema[rebuild the schema cache]'
}

_dbq_cmd_hosts() {
  _arguments \
    '(-c --config)'{-c,--config}'[config file path]:file:_files' \
    '(-o --output)'{-o,--output}'[output format]:format:(text json)'
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

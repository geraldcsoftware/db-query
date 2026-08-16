package nvimpane

import (
	"os"
	"path/filepath"
)

// setupLua is db-query's own required wiring, pushed as one chunk after the UI
// attaches. It runs after any user init.lua precisely so the options the pane
// depends on are the ones that end up in force.
//
// It takes the RPC channel id as its argument rather than assuming channel 1,
// which is only the host's channel when nothing else has connected first.
const setupLua = `
local chan = ...

-- The dialect the bundled sql syntax file reads. It has to precede the filetype
-- set: the syntax file reads this variable when it is sourced, and nothing
-- re-sources it afterwards.
vim.g.sql_type_default = 'sqlanywhere'

vim.bo.filetype = 'sql'
vim.bo.buftype  = 'nofile'
vim.bo.swapfile = false

-- The host draws the pane's border and its label row, so Neovim draws only the
-- text and its own line-number gutter.
vim.wo.number    = true
vim.o.laststatus = 0
vim.o.ruler      = false
vim.o.showmode   = false

-- laststatus = 0 does not reclaim the command-line row; cmdheight does. At
-- cmdheight = 1 a window holds every grid row but one, and the Query pane is
-- only ever a few rows tall, so a permanently blank command line would cost
-- most of it. A ':' command still borrows the bottom row while it is being
-- typed and gives it straight back.
vim.o.cmdheight = 0

-- The ~ end-of-buffer filler is most of a short pane, and the textarea this
-- replaces never showed one.
vim.opt.fillchars:append({ eob = ' ' })

-- Set a second time, the spawn line having set it already, so a user init.lua
-- that assigns shortmess wholesale cannot put a welcome screen back inside the
-- pane. Appending a flag that is already present is a no-op.
vim.opt.shortmess:append('I')

-- Completion triggers as the user types, from a function source, which is
-- Neovim's own mechanism rather than a plugin or a hand-rolled autocommand
-- pair. noselect leaves the first candidate uninserted, so typing on past the
-- popup writes what was typed.
vim.o.completeopt  = 'menu,popup,noselect'
vim.bo.complete    = 'F'
vim.o.autocomplete = true

-- The Lua half of completion gathers context and forwards it; every decision
-- about what to offer is taken in Go, where the tests reach it. base is not
-- forwarded: under refresh = "always" it stays frozen at the text captured on
-- the session's first call, so the prefix is derived from the live line and
-- column instead.
function _G.__dbq_complete(findstart, base)
  return vim.rpcrequest(
    chan, 'dbq_complete',
    findstart,
    vim.api.nvim_get_current_line(),
    vim.fn.col('.'),
    vim.api.nvim_buf_get_lines(0, 0, -1, false)
  )
end
vim.bo.completefunc = 'v:lua.__dbq_complete'

-- The host's mirror of the buffer, so reading the query back costs no round
-- trip. rpcnotify rather than rpcrequest: Neovim never waits on the host for
-- this, and a dropped notification cannot stall the editor.
vim.api.nvim_create_autocmd({ 'TextChanged', 'TextChangedI', 'TextChangedP' }, {
  buffer = 0,
  callback = function()
    vim.rpcnotify(chan, 'dbq_buffer', vim.api.nvim_buf_get_lines(0, 0, -1, false))
  end,
})
`

// ConfigPath is the init.lua db-query reads, if the user has written one. It
// follows from NVIM_APPNAME being set to dbquery: Neovim resolves its config
// directory under that name, so this is the file it sources at startup.
//
// db-query never writes this file. It carries user preference only — a
// colourscheme, mappings, options — and the setup push lands after it.
func ConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "dbquery", "init.lua")
}

// HasConfig reports whether that init.lua exists.
func HasConfig() bool {
	path := ConfigPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

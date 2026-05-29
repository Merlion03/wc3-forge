import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { SessionPool, BridgeError } from "./bridge_client.js";
import { BridgeNotRunningError, listSessions } from "./discovery.js";

const pool = new SessionPool();

const formatResult = (data: unknown): string => JSON.stringify(data, null, 2);

const errorResponse = (err: unknown) => {
  let text: string;
  if (err instanceof BridgeNotRunningError) {
    text = `wc3-forge bridge unavailable.\n${err.message}`;
  } else if (err instanceof BridgeError) {
    text = `Bridge error ${err.code}: ${err.message}`;
  } else if (err instanceof Error) {
    text = `Tool failed: ${err.message}`;
  } else {
    text = `Tool failed: ${String(err)}`;
  }
  return {
    isError: true,
    content: [{ type: "text" as const, text }],
  };
};

const wrap =
  (method: string) =>
  async (args: Record<string, unknown>) => {
    try {
      const result = await pool.call(method, args);
      return { content: [{ type: "text" as const, text: formatResult(result) }] };
    } catch (err) {
      return errorResponse(err);
    }
  };

const SELECTION_ITEM = z.object({
  kind: z
    .string()
    .describe("'unit' | 'item' | 'doodad' | 'region' | 'trigger' — entity kind"),
  id: z
    .number()
    .int()
    .describe("creation_number for unit/item/doodad; opaque per kind"),
});

export function registerTools(server: McpServer): void {
  // --- session management (multi-instance wc3-forge) ----------------------
  server.tool(
    "sessions_list",
    "List all running wc3-forge instances detected via their MCP lockfiles. For each one, also reports the bridge_ping result. Useful when multiple wc3-forge windows are open and Claude needs to pick which one to drive.",
    {},
    async () => {
      try {
        const locks = await listSessions();
        const selected = pool.selected();
        const enriched = await Promise.all(
          locks.map(async (l) => {
            let ping: unknown = null;
            try {
              const saved = pool.selected();
              pool.select(l.pid);
              ping = await pool.call("bridge.ping", {});
              if (saved !== null) pool.select(saved);
              else pool.clearSelection();
            } catch (err) {
              ping = { error: err instanceof Error ? err.message : String(err) };
            }
            return {
              pid: l.pid,
              port: l.port,
              started_at: l.started_at,
              is_selected: l.pid === selected,
              ping,
            };
          })
        );
        return {
          content: [
            {
              type: "text",
              text: formatResult({
                count: enriched.length,
                selected_pid: selected,
                sessions: enriched,
              }),
            },
          ],
        };
      } catch (err) {
        return errorResponse(err);
      }
    }
  );

  server.tool(
    "session_select",
    "Select which wc3-forge instance subsequent tool calls go to, by pid (from sessions_list). Pass pid=null (or omit) to clear and auto-route to the oldest running instance.",
    { pid: z.number().int().nullable().optional() },
    async (args) => {
      try {
        const pid = args.pid;
        if (pid === null || pid === undefined) {
          pool.clearSelection();
          return {
            content: [
              { type: "text", text: "Selection cleared; calls route to the oldest running wc3-forge." },
            ],
          };
        }
        const sessions = await listSessions();
        const match = sessions.find((s) => s.pid === pid);
        if (!match) {
          throw new BridgeNotRunningError(
            `No wc3-forge with pid=${pid}. Active pids: ${sessions.map((s) => s.pid).join(", ") || "(none)"}.`
          );
        }
        pool.select(pid);
        return {
          content: [
            {
              type: "text",
              text: formatResult({ selected_pid: pid, port: match.port, started_at: match.started_at }),
            },
          ],
        };
      } catch (err) {
        return errorResponse(err);
      }
    }
  );

  // --- bridge -------------------------------------------------------------
  server.tool(
    "bridge_ping",
    "Ping the currently-selected wc3-forge bridge. Returns version + map_loaded flag.",
    {},
    wrap("bridge.ping")
  );

  // --- map lifecycle ------------------------------------------------------
  server.tool(
    "map_status",
    "Status of the currently-loaded map (loaded flag, path, name, unit count).",
    {},
    wrap("map.status")
  );
  server.tool(
    "map_open",
    "Load a map into wc3-forge. Accepts an extracted folder OR a .w3x / .w3m / .mpq archive path.",
    { path: z.string().describe("Absolute path to a map folder or .w3x/.w3m/.mpq archive") },
    wrap("map.open")
  );
  server.tool(
    "map_close",
    "Close the currently-loaded map. No-op if nothing is loaded.",
    {},
    wrap("map.close")
  );
  server.tool(
    "map_save",
    "Save the current map in place. Folder-backed maps write directly; MPQ-backed maps return an error (extract to a folder first).",
    {},
    wrap("map.save")
  );

  // --- map info -----------------------------------------------------------
  server.tool(
    "map_info_get",
    "Read war3map.w3i (name, author, description, suggested_players, dimensions, players, …).",
    {},
    wrap("map.info_get")
  );
  server.tool(
    "map_info_set",
    "Partial-update map_info. Currently supported keys: name, author, description, suggestedPlayers (strings), lua (bool). Wire shape: { updates: { …keys } }; returns { changed_fields: N }. Recorded on the undo history — reversible via history_undo.",
    {
      updates: z
        .record(z.unknown())
        .describe("Subset of {name, author, description, suggestedPlayers, lua}"),
    },
    wrap("map.info_set")
  );

  // --- units (placed instances) ------------------------------------------
  server.tool(
    "units_list",
    "List placed units from war3mapUnits.doo. Returns creation_number, type_id, skin_id, player, position [x,y,z], rotation (radians), scale [sx,sy,sz], HP/mana pct, hero level, gold (for gold mines), and inventory slots. Pass `filter.type_id` (4-char rawcode) or `filter.player` (0-based slot; 27 = neutral) to narrow the result — recommended on maps with hundreds of units to keep payloads small.",
    {
      filter: z
        .object({
          type_id: z.string().length(4).optional(),
          player: z.number().int().min(0).optional(),
        })
        .optional(),
    },
    wrap("units.list")
  );
  server.tool(
    "units_get",
    "Read a single placed unit by creation_number (returned from units_list).",
    { creation_number: z.number().int() },
    wrap("units.get")
  );
  server.tool(
    "units_move",
    "Move a placed unit to (x, y, z) in WC3 game coordinates (origin at map center).",
    {
      creation_number: z.number().int(),
      x: z.number(),
      y: z.number(),
      z: z.number(),
    },
    wrap("units.move")
  );
  server.tool(
    "units_rotate",
    "Set a unit's facing angle, in radians around the Z axis.",
    {
      creation_number: z.number().int(),
      rotation: z.number().describe("radians, Z-axis only"),
    },
    wrap("units.rotate")
  );
  server.tool(
    "units_scale",
    "Set per-axis scale on a unit (1.0 = default).",
    {
      creation_number: z.number().int(),
      sx: z.number(),
      sy: z.number(),
      sz: z.number(),
    },
    wrap("units.scale")
  );

  // --- doodads (placed instances) ----------------------------------------
  server.tool(
    "doodads_list",
    "List placed doodads + destructibles from war3map.doo.",
    {},
    wrap("doodads.list")
  );
  server.tool(
    "doodads_get",
    "Read a single placed doodad by creation_number.",
    { creation_number: z.number().int() },
    wrap("doodads.get")
  );
  server.tool(
    "doodads_move",
    "Move a placed doodad to (x, y, z) in WC3 game coordinates.",
    {
      creation_number: z.number().int(),
      x: z.number(),
      y: z.number(),
      z: z.number(),
    },
    wrap("doodads.move")
  );
  server.tool(
    "doodads_rotate",
    "Set a doodad's facing angle, in radians around the Z axis.",
    {
      creation_number: z.number().int(),
      rotation: z.number(),
    },
    wrap("doodads.rotate")
  );
  server.tool(
    "doodads_scale",
    "Set per-axis scale on a doodad. Note: doodad scale is stored raw on disk (no /128 divide that units use).",
    {
      creation_number: z.number().int(),
      sx: z.number(),
      sy: z.number(),
      sz: z.number(),
    },
    wrap("doodads.scale")
  );

  // --- selection ----------------------------------------------------------
  server.tool(
    "selection_get",
    "Read the current editor selection — same selection state shown in the GUI side panels.",
    {},
    wrap("selection.get")
  );
  server.tool(
    "selection_set",
    "Replace the current selection. By default the last item in the list becomes the primary selection; pass 'primary' to override — either an index into items, or a 'kind:id' / bare-'id' selector string.",
    {
      items: z.array(SELECTION_ITEM),
      primary: z
        .union([z.number().int(), z.string()])
        .optional()
        .describe(
          "Optional primary selection: an index into items, or a 'kind:id' (e.g. 'unit:42') / bare 'id' selector. Defaults to the last item."
        ),
    },
    wrap("selection.set")
  );
  server.tool(
    "selection_clear",
    "Clear the current selection.",
    {},
    wrap("selection.clear")
  );

  // --- view + camera ------------------------------------------------------
  server.tool(
    "view_set_mode",
    "Toggle the viewport's editing mode. Accepts 'terrain' or 'doodad' (the underlying handler toggles state — call once per change). Records the requested mode on the session; read it back with view_get_mode.",
    { mode: z.enum(["terrain", "doodad"]).optional() },
    wrap("view.set_mode")
  );
  server.tool(
    "view_get_mode",
    "Read the session's record of the viewport editing mode ('terrain' | 'doodad'). Tracks the last view_set_mode request (defaults to 'doodad'); the live renderer toggle is owned by the frontend, so this reflects the last request rather than a user's manual toolbar click.",
    {},
    wrap("view.get_mode")
  );
  server.tool(
    "view_set_doodad_category_visible",
    "Toggle visibility of a doodad category in the viewport. Category matches the View menu's entries (e.g. 'Trees/Destructibles', 'Structures', or '*' for all).",
    {
      category: z.string(),
      visible: z.boolean(),
    },
    wrap("view.set_doodad_category_visible")
  );
  server.tool(
    "camera_set_view",
    "Pan the camera to (x, y, z) game coordinates. Pass distance > 0 to also set zoom; omit (or 0) to leave distance untouched.",
    {
      x: z.number(),
      y: z.number(),
      z: z.number(),
      distance: z.number().optional(),
    },
    wrap("camera.set_view")
  );

  // --- window -------------------------------------------------------------
  server.tool(
    "window_set_title",
    "Set the agent label segment in the OS window title. Lets multiple parallel agents identify their own instance at a glance. Pass an empty string to clear.",
    { label: z.string() },
    wrap("window.set_title")
  );

  // --- history (undo / redo / groups) ------------------------------------
  server.tool(
    "history_undo",
    "Revert the most recent mutation. Returns { ok, label } where label describes what was undone.",
    {},
    wrap("history.undo")
  );
  server.tool(
    "history_redo",
    "Re-apply the most recently undone mutation.",
    {},
    wrap("history.redo")
  );
  server.tool(
    "history_list",
    "Return the current undo/redo stacks (oldest-first) with their labels.",
    {},
    wrap("history.list")
  );
  server.tool(
    "history_begin_group",
    "Start an undo transaction. Subsequent mutations until history_end_group land in one undo step. Nested begins increment depth — match each with an end.",
    { label: z.string().optional() },
    wrap("history.begin_group")
  );
  server.tool(
    "history_end_group",
    "Close the outermost undo group (or decrement nesting depth).",
    {},
    wrap("history.end_group")
  );

  // --- object data (unit definitions only — w3u shadow is read-only today)
  server.tool(
    "objects_units_list",
    "List all unit definitions (stock SLK rows + per-map w3u overrides). Returns id, name, race, kind (unit/hero/building/special), category, is_custom, is_edited, base_id.",
    {},
    wrap("objects.units.list")
  );
  server.tool(
    "objects_units_get",
    "Return the full field map for a unit definition by id (4-char rawcode). Includes raw values, display-resolved values, and category metadata.",
    { id: z.string().describe("4-char unit rawcode, e.g. 'hfoo' or a custom id") },
    wrap("objects.units.get")
  );
  server.tool(
    "objects_units_fields_meta",
    "Return UnitMetaData — the schema for unit-definition fields (id, field, display_name, category, type). Used to drive a dynamic editor UI.",
    {},
    wrap("objects.units.fields_meta")
  );
}

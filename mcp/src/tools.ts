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

const wrap = (method: string) => {
  const handler = async (args: Record<string, unknown>) => {
    try {
      const result = await pool.call(method, args);
      return { content: [{ type: "text" as const, text: formatResult(result) }] };
    } catch (err) {
      return errorResponse(err);
    }
  };
  // Tag the handler with its bridge method so the Go-catalog generator
  // (mcp/scripts/gen-tools.mjs) can recover the exact method per tool. The
  // tool-name→method mapping is NOT mechanical (e.g. triggers_add_eca maps to
  // triggers.add_eca, not triggers.add.eca), so it must be captured, not guessed.
  // Runtime-harmless: nothing reads this property at MCP-serving time.
  (handler as unknown as { __bridgeMethod?: string }).__bridgeMethod = method;
  return handler;
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
  server.tool(
    "diagnostics_get",
    "Read the running viewport's live render diagnostics (pushed ~5Hz from the frontend): frame counter, fps, camera eye/pivot/distance/yaw/pitch, doodad/unit/visible counts, terrain dimensions + center offset, sky/water flags, last GL error + the faulting render stage, GPU renderer string, and document focus/visibility. The response also carries age_ms (snapshot staleness); a large age_ms means the render loop or reporting has stalled. Lets an agent inspect the editor's actual on-screen state without a screenshot. No params.",
    {},
    wrap("diagnostics.get")
  );
  server.tool(
    "diagnostics_arm",
    "Arm or disarm the viewport's diagnostics mode remotely (same as pressing F9). When armed, the per-frame GL-error stage attribution and other expensive probes run, and the diagnostics overlay shows. Typical flow: arm → reproduce the issue → diagnostics_get → disarm. Pass { on: true } to arm, { on: false } to disarm.",
    { on: z.boolean().describe("true to arm diagnostics mode, false to disarm") },
    wrap("diagnostics.arm")
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

  // --- object data (definitions) — all 7 kinds via the objects.<kind>.* wire
  // namespace. registerObjectKind stamps out the six standard handlers per
  // kind, matching registerObjectKind in handlers_objects.go exactly.
  // NOTE: objects.units.* (list/get/fields_meta) used to be hand-registered
  // here under objects_units_* names; those are now produced by the loop
  // below so the full set (set_field/create_custom/delete_custom) is reachable
  // and every kind is symmetric.
  for (const kind of OBJECT_KINDS) {
    registerObjectKind(server, kind);
  }
  // Cross-kind conversion (doodad↔destructable today). Symmetric wire shape:
  // caller names src/dst kinds in params.
  server.tool(
    "objects_convert",
    "Convert one object definition to another kind, creating a new custom in the destination kind. Today only doodads↔destructables is accepted. Returns { id, detail } for the newly-created custom.",
    {
      src_kind: z
        .enum(OBJECT_KINDS)
        .describe("source kind, e.g. 'doodads'"),
      src_id: z.string().describe("4-char rawcode of the source object"),
      dst_kind: z
        .enum(OBJECT_KINDS)
        .describe("destination kind, e.g. 'destructables'"),
    },
    wrap("objects.convert")
  );

  // --- model import ------------------------------------------------------
  server.tool(
    "models_import",
    "Convert a 3D model file (.obj/.gltf/.glb/.stl) to a minimal WC3 MDX with baked BLP textures and import both into the loaded map under war3mapImported\\. Optionally assigns the imported model to an object's 'file' field. Returns { model_path, texture_paths, warnings }.",
    {
      source_path: z
        .string()
        .describe("Absolute path to the source .obj/.gltf/.glb/.stl file"),
      scale: z
        .number()
        .optional()
        .describe("Uniform scale applied to the mesh (default 1.0)"),
      up_axis: z
        .enum(["y", "z", "auto"])
        .optional()
        .describe("Source up-axis: 'y' (OBJ/glTF default, remaps to Z-up), 'z' (identity), or 'auto'"),
      flip_v: z
        .boolean()
        .optional()
        .describe("Flip texture V coordinate (v -> 1-v) for sources with bottom-origin UVs"),
      target_object_kind: z
        .string()
        .optional()
        .describe("Optional: object kind to assign the model to (units/items/abilities/buffs/destructables/doodads/upgrades)"),
      target_object_id: z
        .string()
        .optional()
        .describe("Optional: 4-char rawcode of the object to set 'file' on (requires target_object_kind)"),
    },
    wrap("models.import")
  );

  // --- triggers (Trigger Editor) -----------------------------------------
  registerTriggerTools(server);

  // --- entity create/delete + terrain (NEW contract methods) -------------
  // These mirror the WIRE-METHOD CONTRACT shared with the entity + terrain
  // agents. Some may not be on the currently-built binary yet; the tool will
  // simply error at call time until the matching Go handler ships.
  registerContractTools(server);
}

// ===========================================================================
// Object Editor — per-kind tool factory.
// ===========================================================================

// OBJECT_KINDS is the exact set of KindConfig.Kind wire tags from
// internal/forge/objects.go. Order matches handlers.go's RegisterAll calls.
// 'destructables' is spelled the WC3-native way (not 'destructibles') to match
// the wire — do not "correct" it.
const OBJECT_KINDS = [
  "units",
  "items",
  "abilities",
  "buffs",
  "destructables",
  "doodads",
  "upgrades",
] as const;

type ObjectKind = (typeof OBJECT_KINDS)[number];

// registerObjectKind stamps out the six standard object-definition tools for a
// single kind, mirroring registerObjectKind(reg, cfg) in handlers_objects.go:
//   objects.<kind>.list / .get / .fields_meta
//   objects.<kind>.set_field / .create_custom / .delete_custom
function registerObjectKind(server: McpServer, kind: ObjectKind): void {
  const rc = "4-char rawcode";
  server.tool(
    `objects_${kind}_list`,
    `List all ${kind} definitions (stock SLK rows + per-map object-data overrides). Returns id, name, race, kind, category, is_custom, is_edited, base_id, icon_art — flat rows the UI groups into a tree client-side.`,
    {},
    wrap(`objects.${kind}.list`)
  );
  server.tool(
    `objects_${kind}_get`,
    `Return the full field map for one ${kind} definition by id (${rc}). Includes raw values, display-resolved values (with and without WC3 color codes), per-field category/type metadata, and the model/icon paths.`,
    { id: z.string().describe(`${rc} of the ${kind} object`) },
    wrap(`objects.${kind}.get`)
  );
  server.tool(
    `objects_${kind}_fields_meta`,
    `Return the field schema (MetaData) for ${kind} definitions: id, field (SLK column), display_name, category, type, min_val, max_val. Drives a dynamic editor UI.`,
    {},
    wrap(`objects.${kind}.fields_meta`)
  );
  server.tool(
    `objects_${kind}_set_field`,
    `Set one field on a ${kind} definition (writes the per-map object-data shadow; undo-aware). Re-emits the post-mutation get payload. Pass an optional 'level' for leveled fields (e.g. ability levels); omit for non-leveled fields.`,
    {
      id: z.string().describe(`${rc} of the ${kind} object to edit`),
      column: z.string().describe("SLK column name (lowercased), e.g. 'name', 'hp'"),
      value: z.string().describe("raw cell value to write (stringified)"),
      level: z
        .number()
        .int()
        .optional()
        .describe("optional 1-based level for leveled fields (ability/upgrade); omit for flat fields"),
    },
    wrap(`objects.${kind}.set_field`)
  );
  server.tool(
    `objects_${kind}_create_custom`,
    `Create a new custom ${kind} definition cloned from a base object. Returns { id, detail } where id is the new custom's rawcode. Undo-aware.`,
    {
      base_id: z.string().describe(`${rc} of the base object to clone from`),
      id: z
        .string()
        .optional()
        .describe(`optional explicit ${rc} for the new custom; auto-assigned when omitted`),
    },
    wrap(`objects.${kind}.create_custom`)
  );
  server.tool(
    `objects_${kind}_delete_custom`,
    `Delete a custom ${kind} definition by id (${rc}). Only custom objects can be deleted; stock rows error. Undo-aware. Returns { ok: true }.`,
    { id: z.string().describe(`${rc} of the custom ${kind} object to delete`) },
    wrap(`objects.${kind}.delete_custom`)
  );
}

// ===========================================================================
// Trigger Editor — all 38 triggers.* methods.
// ===========================================================================

// ID + parent shapes shared across many trigger tools.
const ECA_TYPE = z
  .number()
  .int()
  .describe("wtg.ECAType: 0=event, 1=condition, 2=action, 3=call");
const PARAM_TYPE = z
  .number()
  .int()
  .describe("wtg.ParamType: 0=preset, 1=variable, 2=function, 3=string");

function registerTriggerTools(server: McpServer): void {
  // --- read ---------------------------------------------------------------
  server.tool(
    "triggers_tree",
    "Return the full trigger tree (categories + triggers + variables) for the loaded map in one shot — a flat node array the UI stitches into a hierarchy via parent_id. Empty (non-error) when no map / no triggers.",
    {},
    wrap("triggers.tree")
  );
  server.tool(
    "triggers_get",
    "Return one trigger node's full detail by id: category, trigger (with nested ECAs + sub-parameters + custom_text), or variable. Exactly one is populated; 'kind' disambiguates.",
    { id: z.number().int().describe("node id from triggers_tree") },
    wrap("triggers.get")
  );
  server.tool(
    "triggers_functions_meta",
    "Return the TriggerData.txt vocabulary: every event/condition/action/call function, plus category labels, type metadata, and presets. Large, static payload used to render GUI labels client-side.",
    {},
    wrap("triggers.functions_meta")
  );
  server.tool(
    "triggers_search",
    "Fuzzy substring search across all triggers. Empty query returns the first N triggers by name. Returns { hits, truncated }.",
    {
      query: z.string().describe("substring to match (empty = first N by name)"),
      limit: z.number().int().optional().describe("max hits (default 50)"),
    },
    wrap("triggers.search")
  );

  // --- structural adds ----------------------------------------------------
  server.tool(
    "triggers_add_category",
    "Add a trigger category (folder). Returns the refreshed tree + the new node's detail.",
    {
      name: z.string().describe("category display name"),
      parent_id: z.number().int().describe("parent node id (0 / map-root for top level)"),
    },
    wrap("triggers.add_category")
  );
  server.tool(
    "triggers_add_gui",
    "Add a normal GUI trigger (holds ECAs). Returns the refreshed tree + the new trigger's detail.",
    {
      name: z.string().describe("trigger name"),
      parent_id: z.number().int().describe("parent category id"),
    },
    wrap("triggers.add_gui")
  );
  server.tool(
    "triggers_add_script",
    "Add a custom-script (JASS/Lua) trigger. Returns the refreshed tree + the new trigger's detail.",
    {
      name: z.string().describe("trigger name"),
      parent_id: z.number().int().describe("parent category id"),
    },
    wrap("triggers.add_script")
  );
  server.tool(
    "triggers_add_comment",
    "Add a comment trigger node. Returns the refreshed tree + the new node's detail.",
    {
      name: z.string().describe("comment text / name"),
      parent_id: z.number().int().describe("parent category id"),
    },
    wrap("triggers.add_comment")
  );
  server.tool(
    "triggers_add_variable",
    "Add a global trigger variable (udg_*). Returns the refreshed tree + the new variable's detail.",
    {
      name: z.string().describe("variable name (without udg_ prefix)"),
      type: z.string().describe("WC3 variable type, e.g. 'integer', 'unit', 'real'"),
      is_array: z.boolean().describe("true for an array variable"),
      array_size: z.number().int().describe("array size when is_array (else 0)"),
      initial_value: z.string().describe("initial value literal ('' for none)"),
    },
    wrap("triggers.add_variable")
  );

  // --- node-level edits ---------------------------------------------------
  server.tool(
    "triggers_delete",
    "Delete a trigger node (category / trigger / variable) by id. Returns the refreshed tree.",
    { id: z.number().int().describe("node id to delete") },
    wrap("triggers.delete")
  );
  server.tool(
    "triggers_rename",
    "Rename a trigger node by id. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("node id"),
      name: z.string().describe("new name"),
    },
    wrap("triggers.rename")
  );
  server.tool(
    "triggers_move",
    "Re-parent a trigger node under a new parent. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("node id to move"),
      new_parent_id: z.number().int().describe("destination parent node id"),
    },
    wrap("triggers.move")
  );
  server.tool(
    "triggers_set_enabled",
    "Toggle a trigger's enabled flag. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("trigger id"),
      value: z.boolean().describe("true = enabled"),
    },
    wrap("triggers.set_enabled")
  );
  server.tool(
    "triggers_set_initially_on",
    "Toggle a trigger's 'initially on' flag. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("trigger id"),
      value: z.boolean().describe("true = initially on"),
    },
    wrap("triggers.set_initially_on")
  );
  server.tool(
    "triggers_set_run_on_init",
    "Toggle a trigger's 'run on map initialization' flag. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("trigger id"),
      value: z.boolean().describe("true = run on initialization"),
    },
    wrap("triggers.set_run_on_init")
  );
  server.tool(
    "triggers_set_description",
    "Set a trigger's description text. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("trigger id"),
      text: z.string().describe("description text"),
    },
    wrap("triggers.set_description")
  );
  server.tool(
    "triggers_set_custom_text",
    "Set a custom-script trigger's raw script text. Returns the refreshed tree + the node's detail.",
    {
      id: z.number().int().describe("trigger id (a script trigger)"),
      text: z.string().describe("JASS/Lua source text"),
    },
    wrap("triggers.set_custom_text")
  );
  server.tool(
    "triggers_set_variable",
    "Edit an existing trigger variable's name/type/array/initial-value. Returns the refreshed tree + the variable's detail.",
    {
      id: z.number().int().describe("variable node id"),
      name: z.string().describe("variable name"),
      type: z.string().describe("WC3 variable type"),
      is_array: z.boolean(),
      array_size: z.number().int(),
      initial_value: z.string(),
    },
    wrap("triggers.set_variable")
  );
  server.tool(
    "triggers_set_map_header_script",
    "Set the map-header custom script (the global JASS/Lua block at the top of the map's script). Returns the refreshed tree.",
    { content: z.string().describe("header script source text") },
    wrap("triggers.set_map_header_script")
  );

  // --- ECA-list mutation --------------------------------------------------
  server.tool(
    "triggers_add_eca",
    "Add a top-level ECA (event/condition/action/call) to a trigger. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_type: ECA_TYPE,
      name: z.string().describe("function name from triggers_functions_meta"),
      position: z
        .number()
        .int()
        .optional()
        .describe("insert index within the ECA group; omit to append"),
    },
    wrap("triggers.add_eca")
  );
  server.tool(
    "triggers_add_nested_eca",
    "Add an ECA nested inside a magic/container ECA (IfThenElse Then-branch, ForLoop body, etc.). Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      parent_path: z
        .array(z.number().int())
        .describe("ECA path to the container ECA (array of child indices)"),
      eca_type: ECA_TYPE,
      name: z.string().describe("function name"),
      group_id: z
        .number()
        .int()
        .describe("magic-ECA child group id (selects which branch/body)"),
    },
    wrap("triggers.add_nested_eca")
  );
  server.tool(
    "triggers_move_eca",
    "Move an ECA within its sibling group to a new position. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z
        .array(z.number().int())
        .describe("ECA path (array of child indices) addressing the ECA to move"),
      new_position: z.number().int().describe("new index within the sibling group"),
    },
    wrap("triggers.move_eca")
  );
  server.tool(
    "triggers_delete_eca",
    "Delete an ECA (and its nested children) by path. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z
        .array(z.number().int())
        .describe("ECA path (array of child indices)"),
    },
    wrap("triggers.delete_eca")
  );
  server.tool(
    "triggers_set_eca_enabled",
    "Toggle an ECA's enabled flag. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z.array(z.number().int()).describe("ECA path (array of child indices)"),
      enabled: z.boolean(),
    },
    wrap("triggers.set_eca_enabled")
  );

  // --- parameter mutation -------------------------------------------------
  server.tool(
    "triggers_set_param_value",
    "Set one parameter slot's value inside an ECA. Use param_path (array, addresses sub-parameter chains) OR the legacy param_index (single int). Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z.array(z.number().int()).describe("ECA path (array of child indices)"),
      param_path: z
        .array(z.number().int())
        .optional()
        .describe("2b2 shape: addresses sub-parameter chains; wins over param_index"),
      param_index: z
        .number()
        .int()
        .optional()
        .describe("legacy single-index shape; used when param_path omitted"),
      value: z.string().describe("the value literal / variable name / preset key / function name"),
      param_type: PARAM_TYPE,
    },
    wrap("triggers.set_param_value")
  );
  server.tool(
    "triggers_set_param_sub_function",
    "Attach a sub-function (nested call) to a parameter slot. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z.array(z.number().int()).describe("ECA path (array of child indices)"),
      param_path: z.array(z.number().int()).describe("parameter path (sub-parameter chain)"),
      sub_name: z.string().describe("sub-function name from triggers_functions_meta"),
    },
    wrap("triggers.set_param_sub_function")
  );
  server.tool(
    "triggers_clear_param_sub_function",
    "Remove a sub-function from a parameter slot, reverting it to a plain value. Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z.array(z.number().int()).describe("ECA path (array of child indices)"),
      param_path: z.array(z.number().int()).describe("parameter path (sub-parameter chain)"),
    },
    wrap("triggers.clear_param_sub_function")
  );
  server.tool(
    "triggers_set_param_array",
    "Toggle a parameter slot's array-index flag (so it reads var[index] vs var). Returns the refreshed tree + the trigger's detail.",
    {
      trigger_id: z.number().int().describe("target trigger id"),
      eca_path: z.array(z.number().int()).describe("ECA path (array of child indices)"),
      param_path: z.array(z.number().int()).describe("parameter path (sub-parameter chain)"),
      is_array: z.boolean(),
    },
    wrap("triggers.set_param_array")
  );

  // --- entity instance pickers (read-only) -------------------------------
  server.tool(
    "triggers_list_regions",
    "List the map's regions (war3map.w3r) with their gg_rct_* codegen names — for region-typed parameter pickers.",
    {},
    wrap("triggers.list_regions")
  );
  server.tool(
    "triggers_list_cameras",
    "List the map's camera presets (war3map.w3c) with their gg_cam_* codegen names — for camera-typed parameter pickers.",
    {},
    wrap("triggers.list_cameras")
  );
  server.tool(
    "triggers_list_unit_instances",
    "List placed unit instances with their gg_unit_* codegen names — for unit-typed parameter pickers.",
    {},
    wrap("triggers.list_unit_instances")
  );
  server.tool(
    "triggers_list_destructable_instances",
    "List placed destructable instances with their gg_dest_* codegen names — for destructable-typed parameter pickers.",
    {},
    wrap("triggers.list_destructable_instances")
  );

  // --- codegen / convert / test ------------------------------------------
  server.tool(
    "triggers_generate_script",
    "Generate the war3map.lua script text from the current triggers WITHOUT writing it. Returns { text, bytes }.",
    {},
    wrap("triggers.generate_script")
  );
  server.tool(
    "triggers_save_script",
    "Generate + write war3map.lua to the map source. Returns { bytes }. Errors for MPQ-backed maps (extract to a folder first).",
    {},
    wrap("triggers.save_script")
  );
  server.tool(
    "triggers_test_map",
    "Full Test Map pipeline: save pending edits, regenerate + write war3map.lua, then launch WC3 with the map preloaded. Returns { ok: true }.",
    {},
    wrap("triggers.test_map")
  );
  server.tool(
    "triggers_transpile_preview",
    "Return the read-only Convert-to-Lua diff preview (vJASS → Lua) without writing anything.",
    {},
    wrap("triggers.transpile_preview")
  );
  server.tool(
    "triggers_check_convert_to_lua",
    "Read-only blocker scan for the Convert-Map-to-Lua flow. Always returns a result payload (blockers may be empty); never mutates.",
    {},
    wrap("triggers.check_convert_to_lua")
  );
  server.tool(
    "triggers_convert_to_lua",
    "Run the full Convert-Map-to-Lua conversion. When blockers exist, returns a { blockers: [...] } payload (NOT an error) so the UI can render them. Other failures error. Accepts optional { backup } (default true).",
    {
      backup: z
        .boolean()
        .optional()
        .describe("write a backup of the original script before converting (default true)"),
    },
    wrap("triggers.convert_to_lua")
  );
}

// ===========================================================================
// NEW contract methods — entity create/delete + terrain. Exposed per the
// shared WIRE-METHOD CONTRACT; the matching Go handlers ship from the entity
// and terrain agents. Until then these tools error cleanly at call time.
// ===========================================================================

function registerContractTools(server: McpServer): void {
  server.tool(
    "units_create",
    "Create a new placed unit in war3mapUnits.doo. x/y are WC3 world units (origin = map center). Returns { creation_number }. Undo-aware; emits the standard change events.",
    {
      type_id: z.string().describe("4-char unit rawcode, e.g. 'hfoo'"),
      player: z.number().int().describe("owning player slot (0-based; 27 = neutral hostile)"),
      x: z.number().describe("world X (origin = map center)"),
      y: z.number().describe("world Y (origin = map center)"),
      z: z.number().optional().describe("world Z (default 0 / terrain height)"),
      rotation: z.number().optional().describe("facing angle in radians (Z-axis); default 0"),
      scale: z.number().optional().describe("uniform scale (default 1.0)"),
    },
    wrap("units.create")
  );
  server.tool(
    "units_delete",
    "Delete a placed unit by creation_number. Returns { ok: true }. Undo-aware; emits the standard change events.",
    { creation_number: z.number().int().describe("creation_number from units_list") },
    wrap("units.delete")
  );
  server.tool(
    "doodads_create",
    "Create a new placed doodad/destructable in war3map.doo. x/y are WC3 world units (origin = map center). Returns { creation_number }. Undo-aware; emits the standard change events.",
    {
      type_id: z.string().describe("4-char doodad/destructable rawcode"),
      x: z.number().describe("world X (origin = map center)"),
      y: z.number().describe("world Y (origin = map center)"),
      z: z.number().optional().describe("world Z (default 0 / terrain height)"),
      rotation: z.number().optional().describe("facing angle in radians; default 0"),
      scale: z.number().optional().describe("uniform scale (default 1.0)"),
      variation: z.number().int().optional().describe("doodad variation index (default 0)"),
    },
    wrap("doodads.create")
  );
  server.tool(
    "doodads_delete",
    "Delete a placed doodad/destructable by creation_number. Returns { ok: true }. Undo-aware; emits the standard change events.",
    { creation_number: z.number().int().describe("creation_number from doodads_list") },
    wrap("doodads.delete")
  );
  server.tool(
    "terrain_set_tile",
    "Set the ground texture (tile) at a terrain corner. col/row are 0-based corner grid indices. ground_tile_id is a 4-char FourCC that must be in the map's ground tile palette. Returns { ok: true }. Undo-aware.",
    {
      col: z.number().int().describe("0-based terrain corner column"),
      row: z.number().int().describe("0-based terrain corner row"),
      ground_tile_id: z.string().describe("4-char FourCC in the map ground palette"),
    },
    wrap("terrain.set_tile")
  );
  server.tool(
    "terrain_set_height",
    "Set the height at a terrain corner. col/row are 0-based corner grid indices. Returns { ok: true }. Undo-aware.",
    {
      col: z.number().int().describe("0-based terrain corner column"),
      row: z.number().int().describe("0-based terrain corner row"),
      height: z.number().describe("terrain height value"),
    },
    wrap("terrain.set_height")
  );
  server.tool(
    "terrain_get_tile",
    "Read back the ground tile + height at a terrain corner (so edits are verifiable). col/row are 0-based corner grid indices. Returns { col, row, ground_tile_id, height }.",
    {
      col: z.number().int().describe("0-based terrain corner column"),
      row: z.number().int().describe("0-based terrain corner row"),
    },
    wrap("terrain.get_tile")
  );
  // --- Terrain brushes (footprint edits; the GUI Terrain Palette engine) ---
  // col/row are the footprint CENTER. radius is in corner units (0 = single
  // corner, 1 = 3x3, ...). shape: "circle" (euclidean) or "square" (chebyshev).
  // Each call is one undo step; bracket a series with history_begin_group /
  // history_end_group to collapse a multi-dab edit into one.
  server.tool(
    "terrain_paint_tile",
    "Paint a ground texture (tile) over a brush footprint of terrain corners. col/row are the footprint center (0-based corner indices). radius is in corner units (0 = single corner). shape is 'circle' or 'square'. ground_tile_id is a 4-char FourCC that must be in the map's ground palette. Returns { ok: true }. Undo-aware (one step per call).",
    {
      col: z.number().int().describe("footprint center column (0-based corner)"),
      row: z.number().int().describe("footprint center row (0-based corner)"),
      radius: z.number().optional().describe("brush radius in corner units, fractional allowed (0 = single corner)"),
      shape: z.enum(["circle", "square"]).optional().describe("brush shape (default circle)"),
      ground_tile_id: z.string().describe("4-char FourCC in the map ground palette"),
    },
    wrap("terrain.paint_tile")
  );
  server.tool(
    "terrain_brush_height",
    "Raise/lower/flatten/smooth the terrain heightfield over a brush footprint. mode: 'raise' | 'lower' | 'flatten' | 'smooth'. strength is game-Z units added per dab for raise/lower (default 16), or a 0..1 blend fraction for smooth (default 0.5). target is the height (game-Z) to flatten to. Returns { ok: true }. Undo-aware.",
    {
      col: z.number().int().describe("footprint center column (0-based corner)"),
      row: z.number().int().describe("footprint center row (0-based corner)"),
      radius: z.number().optional().describe("brush radius in corner units, fractional allowed (0 = single corner)"),
      shape: z.enum(["circle", "square"]).optional().describe("brush shape (default circle)"),
      mode: z.enum(["raise", "lower", "flatten", "smooth"]).describe("height brush mode"),
      strength: z.number().optional().describe("game-Z per dab (raise/lower) or 0..1 blend (smooth)"),
      target: z.number().optional().describe("flatten target height (game-Z)"),
    },
    wrap("terrain.brush_height")
  );
  server.tool(
    "terrain_brush_cliff",
    "Raise/lower/set the integer cliff layer (0..15) over a brush footprint. mode: 'raise' (layer+1) | 'lower' (layer-1) | 'set' (layer=level). cliff_tile_id selects the cliff tileset FourCC (empty → slot 0); the brush sets each corner's cliff texture so a wall actually renders. Cliff walls are derived from layer differences, so painting a higher layer next to lower terrain creates a cliff. Returns { ok: true }. Undo-aware.",
    {
      col: z.number().int().describe("footprint center column (0-based corner)"),
      row: z.number().int().describe("footprint center row (0-based corner)"),
      radius: z.number().optional().describe("brush radius in corner units, fractional allowed (0 = single corner)"),
      shape: z.enum(["circle", "square"]).optional().describe("brush shape (default circle)"),
      mode: z.enum(["raise", "lower", "set"]).describe("cliff brush mode"),
      level: z.number().int().optional().describe("for raise/lower: number of cliff steps to move (default 1); for 'set': absolute target layer (0..15)"),
      cliff_tile_id: z.string().optional().describe("4-char cliff-tileset FourCC (empty → slot 0)"),
    },
    wrap("terrain.brush_cliff")
  );
  server.tool(
    "terrain_brush_ramp",
    "Toggle the ramp flag over a brush footprint. on=true makes the cliff step under the footprint render as a walkable slope; on=false reverts it to a sheer wall. Use over a cliff edge after raising a cliff to make it traversable. Returns { ok: true }. Undo-aware.",
    {
      col: z.number().int().describe("footprint center column (0-based corner)"),
      row: z.number().int().describe("footprint center row (0-based corner)"),
      radius: z.number().optional().describe("brush radius in corner units, fractional allowed (0 = single corner)"),
      shape: z.enum(["circle", "square"]).optional().describe("brush shape (default circle)"),
      on: z.boolean().describe("true = mark ramp (slope), false = clear ramp (wall)"),
    },
    wrap("terrain.brush_ramp")
  );
}

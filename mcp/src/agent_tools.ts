import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { SessionPool, BridgeError } from "./bridge_client.js";
import { BridgeNotRunningError } from "./discovery.js";

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
  return { isError: true, content: [{ type: "text" as const, text }] };
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
  // Mirror tools.ts: useful for catalog/debug tooling even though the fork's
  // agent catalog is committed separately from upstream tools.json.
  (handler as unknown as { __bridgeMethod?: string }).__bridgeMethod = method;
  return handler;
};

const POINT = z.object({ x: z.number(), y: z.number() }).strict();
const RECT = z
  .object({ min_x: z.number(), min_y: z.number(), max_x: z.number(), max_y: z.number() })
  .strict();

const PATCH_OPERATION = z
  .object({
    op: z.enum([
      "units.move",
      "units.rotate",
      "units.scale",
      "units.set_field",
      "units.create",
      "units.delete",
      "doodads.move",
      "doodads.rotate",
      "doodads.scale",
      "doodads.create",
      "doodads.delete",
      "regions.create",
      "regions.move",
      "regions.resize",
      "regions.rename",
      "regions.delete",
      "terrain.set_tile",
      "terrain.set_height",
      "terrain.paint_tile",
      "terrain.brush_height",
      "map.info_set",
    ]),
    creation_number: z.number().int().optional(),
    type_id: z.string().length(4).optional(),
    player: z.number().int().min(0).optional(),
    x: z.number().optional(),
    y: z.number().optional(),
    z: z.number().optional(),
    rotation: z.number().optional(),
    scale: z.number().optional(),
    sx: z.number().optional(),
    sy: z.number().optional(),
    sz: z.number().optional(),
    field: z.string().optional(),
    value: z.number().optional(),
    item_drops: z
      .array(z.object({ item_id: z.string().length(4), chance: z.number().int().min(0) }).strict())
      .optional(),
    variation: z.number().int().min(0).optional(),
    name: z.string().optional(),
    min_x: z.number().optional(),
    min_y: z.number().optional(),
    max_x: z.number().optional(),
    max_y: z.number().optional(),
    weather_id: z.string().optional(),
    ambient_id: z.string().optional(),
    color: z.array(z.number().int()).length(3).optional(),
    dx: z.number().optional(),
    dy: z.number().optional(),
    col: z.number().int().optional(),
    row: z.number().int().optional(),
    ground_tile_id: z.string().length(4).optional(),
    height: z.number().optional(),
    radius: z.number().min(0).optional(),
    shape: z.enum(["circle", "square"]).optional(),
    mode: z.enum(["raise", "lower", "flatten", "smooth"]).optional(),
    strength: z.number().optional(),
    target: z.number().optional(),
    updates: z.record(z.unknown()).optional(),
  })
  .strict();

export function registerAgentTools(server: McpServer): void {
  server.tool(
    "scene_query",
    "High-level read-only query across placed units, doodads/destructibles, regions, and start locations. Use this instead of pulling whole per-kind lists when you need spatial or semantic selection. Attribute and spatial filters are ANDed. nearest_to computes true point-to-rectangle distance for regions and sorts by distance by default. id means creation_number for unit/doodad/region and start-location index for start_location. Results are paginated (default 100, max 500); kind+id are always returned even when fields projects the payload.",
    {
      kinds: z.array(z.enum(["unit", "doodad", "region", "start_location"])).optional(),
      where: z
        .object({
          type_id: z.string().length(4).optional(),
          player: z.number().int().min(0).optional(),
          name_contains: z.string().optional(),
          ids: z.array(z.number().int()).optional(),
        })
        .strict()
        .optional(),
      spatial: z
        .object({
          radius: z
            .object({ x: z.number(), y: z.number(), radius: z.number().min(0) })
            .strict()
            .optional(),
          rect: RECT.optional(),
          within_region: z.number().int().optional(),
        })
        .strict()
        .optional(),
      nearest_to: POINT.optional(),
      sort: z.enum(["distance", "kind", "id", "x", "y", "type_id"]).optional(),
      order: z.enum(["asc", "desc"]).optional(),
      limit: z.number().int().min(1).max(500).optional(),
      offset: z.number().int().min(0).optional(),
      fields: z
        .array(
          z.enum([
            "kind",
            "id",
            "creation_number",
            "index",
            "type_id",
            "name",
            "player",
            "position",
            "bounds",
            "rotation",
            "scale",
            "variation",
            "life",
            "flags",
            "distance",
          ])
        )
        .optional(),
    },
    wrap("scene.query")
  );

  server.tool(
    "map_apply_patch",
    "Preflight and optionally apply a batch of WC3 map edits as one atomic undo step. Set dry_run=true to validate schemas, resolve sequential create/delete targets, predict creation numbers, and estimate affected entities without mutating anything. Apply mode runs only after the whole patch preflights; if any runtime operation fails, all earlier operations are rolled back and history/dirty state are restored. Maximum 500 operations per patch; raw sloc start-location markers are intentionally not addressable through units.* operations.",
    {
      label: z.string().optional(),
      dry_run: z.boolean().optional(),
      operations: z.array(PATCH_OPERATION).max(500),
    },
    wrap("map.apply_patch")
  );

  server.tool(
    "map_validate",
    "Run the read-only Map Doctor over the loaded map. Returns stable error/warning codes for concrete structural problems: missing/malformed terrain, invalid palette indices/FourCCs, duplicate creation numbers, duplicate start-location indices, invalid region bounds/weather ids, duplicate/invalid custom object ids, and placements completely outside terrain bounds. Warnings do not make valid=false.",
    {},
    wrap("map.validate")
  );
}

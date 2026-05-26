export namespace doodadsdoo {
	
	export class ItemDrop {
	    ItemID: string;
	    Chance: number;
	
	    static createFrom(source: any = {}) {
	        return new ItemDrop(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ItemID = source["ItemID"];
	        this.Chance = source["Chance"];
	    }
	}
	export class Doodad {
	    TypeID: string;
	    Variation: number;
	    Position: number[];
	    Rotation: number;
	    Scale: number[];
	    SkinID: string;
	    Flags: number;
	    Life: number;
	    MapItemTable: number;
	    ItemDrops: ItemDrop[];
	    CreationNumber: number;
	
	    static createFrom(source: any = {}) {
	        return new Doodad(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.TypeID = source["TypeID"];
	        this.Variation = source["Variation"];
	        this.Position = source["Position"];
	        this.Rotation = source["Rotation"];
	        this.Scale = source["Scale"];
	        this.SkinID = source["SkinID"];
	        this.Flags = source["Flags"];
	        this.Life = source["Life"];
	        this.MapItemTable = source["MapItemTable"];
	        this.ItemDrops = this.convertValues(source["ItemDrops"], ItemDrop);
	        this.CreationNumber = source["CreationNumber"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace forge {
	
	export class HistoryEntry {
	    index: number;
	    label: string;
	    active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.label = source["label"];
	        this.active = source["active"];
	    }
	}
	export class HistoryState {
	    undo: HistoryEntry[];
	    redo: HistoryEntry[];
	
	    static createFrom(source: any = {}) {
	        return new HistoryState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.undo = this.convertValues(source["undo"], HistoryEntry);
	        this.redo = this.convertValues(source["redo"], HistoryEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class BridgeInfo {
	    pid: number;
	    port: number;
	    token_short: string;
	    map_name: string;
	    map_path: string;
	
	    static createFrom(source: any = {}) {
	        return new BridgeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	        this.port = source["port"];
	        this.token_short = source["token_short"];
	        this.map_name = source["map_name"];
	        this.map_path = source["map_path"];
	    }
	}
	export class DoodadDTO {
	    creation_number: number;
	    type_id: string;
	    skin_id: string;
	    position: number[];
	    rotation: number;
	    scale: number[];
	    variation: number;
	    life: number;
	    flags: number;
	
	    static createFrom(source: any = {}) {
	        return new DoodadDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.creation_number = source["creation_number"];
	        this.type_id = source["type_id"];
	        this.skin_id = source["skin_id"];
	        this.position = source["position"];
	        this.rotation = source["rotation"];
	        this.scale = source["scale"];
	        this.variation = source["variation"];
	        this.life = source["life"];
	        this.flags = source["flags"];
	    }
	}
	export class MapStatus {
	    loaded: boolean;
	    path?: string;
	    name?: string;
	    unit_count: number;
	
	    static createFrom(source: any = {}) {
	        return new MapStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loaded = source["loaded"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.unit_count = source["unit_count"];
	    }
	}
	export class MinimapDTO {
	    bytes: string;
	    ext: string;
	    found: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MinimapDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bytes = source["bytes"];
	        this.ext = source["ext"];
	        this.found = source["found"];
	    }
	}
	export class PathingMapDTO {
	    width: number;
	    height: number;
	    cells: number[];
	
	    static createFrom(source: any = {}) {
	        return new PathingMapDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.cells = source["cells"];
	    }
	}
	export class SelectionItemDTO {
	    kind: string;
	    id: number;
	
	    static createFrom(source: any = {}) {
	        return new SelectionItemDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.id = source["id"];
	    }
	}
	export class SelectionDTO {
	    items: SelectionItemDTO[];
	    primary: number;
	
	    static createFrom(source: any = {}) {
	        return new SelectionDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], SelectionItemDTO);
	        this.primary = source["primary"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class WaterInfo {
	    ok: boolean;
	    offset: number;
	    shallow_min: number[];
	    shallow_max: number[];
	    deep_min: number[];
	    deep_max: number[];
	    texture_file: string;
	    num_textures: number;
	    texture_rate: number;
	
	    static createFrom(source: any = {}) {
	        return new WaterInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.offset = source["offset"];
	        this.shallow_min = source["shallow_min"];
	        this.shallow_max = source["shallow_max"];
	        this.deep_min = source["deep_min"];
	        this.deep_max = source["deep_max"];
	        this.texture_file = source["texture_file"];
	        this.num_textures = source["num_textures"];
	        this.texture_rate = source["texture_rate"];
	    }
	}
	export class TerrainDTO {
	    width: number;
	    height: number;
	    center_offset: number[];
	    heights: number[];
	    ground_tex: number[];
	    tileset: string;
	    palette: string[];
	    palette_colors: number[][];
	    palette_textures: string[];
	    layer_height: number[];
	    cliff_tex: number[];
	    cliff_var: number[];
	    ground_var: number[];
	    ramp_flags: number[];
	    cliff_palette: string[];
	    cliff_palette_textures: string[];
	    water_z: number[];
	    has_water: number[];
	    water: WaterInfo;
	    shadow_map: number[];
	    shadow_map_width: number;
	    shadow_map_height: number;
	    cell_skip: number[];
	    cliff_displacement: number[];
	
	    static createFrom(source: any = {}) {
	        return new TerrainDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.center_offset = source["center_offset"];
	        this.heights = source["heights"];
	        this.ground_tex = source["ground_tex"];
	        this.tileset = source["tileset"];
	        this.palette = source["palette"];
	        this.palette_colors = source["palette_colors"];
	        this.palette_textures = source["palette_textures"];
	        this.layer_height = source["layer_height"];
	        this.cliff_tex = source["cliff_tex"];
	        this.cliff_var = source["cliff_var"];
	        this.ground_var = source["ground_var"];
	        this.ramp_flags = source["ramp_flags"];
	        this.cliff_palette = source["cliff_palette"];
	        this.cliff_palette_textures = source["cliff_palette_textures"];
	        this.water_z = source["water_z"];
	        this.has_water = source["has_water"];
	        this.water = this.convertValues(source["water"], WaterInfo);
	        this.shadow_map = source["shadow_map"];
	        this.shadow_map_width = source["shadow_map_width"];
	        this.shadow_map_height = source["shadow_map_height"];
	        this.cell_skip = source["cell_skip"];
	        this.cliff_displacement = source["cliff_displacement"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UnitDTO {
	    creation_number: number;
	    type_id: string;
	    skin_id: string;
	    player: number;
	    position: number[];
	    rotation: number;
	    scale: number[];
	    hit_points_pct: number;
	    mana_pct: number;
	    hero_level: number;
	    gold_amount: number;
	
	    static createFrom(source: any = {}) {
	        return new UnitDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.creation_number = source["creation_number"];
	        this.type_id = source["type_id"];
	        this.skin_id = source["skin_id"];
	        this.player = source["player"];
	        this.position = source["position"];
	        this.rotation = source["rotation"];
	        this.scale = source["scale"];
	        this.hit_points_pct = source["hit_points_pct"];
	        this.mana_pct = source["mana_pct"];
	        this.hero_level = source["hero_level"];
	        this.gold_amount = source["gold_amount"];
	    }
	}

}

export namespace unitsdoo {
	
	export class AbilityMod {
	    AbilityID: string;
	    Autocast: boolean;
	    Level: number;
	
	    static createFrom(source: any = {}) {
	        return new AbilityMod(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.AbilityID = source["AbilityID"];
	        this.Autocast = source["Autocast"];
	        this.Level = source["Level"];
	    }
	}
	export class InventorySlot {
	    Slot: number;
	    ItemID: string;
	
	    static createFrom(source: any = {}) {
	        return new InventorySlot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Slot = source["Slot"];
	        this.ItemID = source["ItemID"];
	    }
	}
	export class ItemDrop {
	    ItemID: string;
	    Chance: number;
	
	    static createFrom(source: any = {}) {
	        return new ItemDrop(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ItemID = source["ItemID"];
	        this.Chance = source["Chance"];
	    }
	}
	export class Entity {
	    TypeID: string;
	    Variation: number;
	    Position: number[];
	    Rotation: number;
	    Scale: number[];
	    SkinID: string;
	    Flags: number;
	    Player: number;
	    UnknownByte1: number;
	    UnknownByte2: number;
	    HitPointsPct: number;
	    ManaPct: number;
	    MapItemTable: number;
	    ItemDrops: ItemDrop[];
	    GoldAmount: number;
	    TargetAcquisition: number;
	    HeroLevel: number;
	    HeroStr: number;
	    HeroAgi: number;
	    HeroInt: number;
	    Inventory: InventorySlot[];
	    AbilityModifications: AbilityMod[];
	    RandomType: number;
	    RandomData: number[];
	    CustomColor: number;
	    WaygateRegion: number;
	    CreationNumber: number;
	
	    static createFrom(source: any = {}) {
	        return new Entity(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.TypeID = source["TypeID"];
	        this.Variation = source["Variation"];
	        this.Position = source["Position"];
	        this.Rotation = source["Rotation"];
	        this.Scale = source["Scale"];
	        this.SkinID = source["SkinID"];
	        this.Flags = source["Flags"];
	        this.Player = source["Player"];
	        this.UnknownByte1 = source["UnknownByte1"];
	        this.UnknownByte2 = source["UnknownByte2"];
	        this.HitPointsPct = source["HitPointsPct"];
	        this.ManaPct = source["ManaPct"];
	        this.MapItemTable = source["MapItemTable"];
	        this.ItemDrops = this.convertValues(source["ItemDrops"], ItemDrop);
	        this.GoldAmount = source["GoldAmount"];
	        this.TargetAcquisition = source["TargetAcquisition"];
	        this.HeroLevel = source["HeroLevel"];
	        this.HeroStr = source["HeroStr"];
	        this.HeroAgi = source["HeroAgi"];
	        this.HeroInt = source["HeroInt"];
	        this.Inventory = this.convertValues(source["Inventory"], InventorySlot);
	        this.AbilityModifications = this.convertValues(source["AbilityModifications"], AbilityMod);
	        this.RandomType = source["RandomType"];
	        this.RandomData = source["RandomData"];
	        this.CustomColor = source["CustomColor"];
	        this.WaygateRegion = source["WaygateRegion"];
	        this.CreationNumber = source["CreationNumber"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

export namespace w3i {
	
	export class CamDistance {
	    Default: number;
	    Max: number;
	    Min: number;
	
	    static createFrom(source: any = {}) {
	        return new CamDistance(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Default = source["Default"];
	        this.Max = source["Max"];
	        this.Min = source["Min"];
	    }
	}
	export class Color {
	    R: number;
	    G: number;
	    B: number;
	    A: number;
	
	    static createFrom(source: any = {}) {
	        return new Color(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.R = source["R"];
	        this.G = source["G"];
	        this.B = source["B"];
	        this.A = source["A"];
	    }
	}
	export class Flags {
	    Raw: number;
	    HideMinimapPreview: boolean;
	    ModifyAllyPriorities: boolean;
	    MeleeMap: boolean;
	    Unknown1: boolean;
	    MaskedAreaPartiallyVisible: boolean;
	    FixedPlayerSettings: boolean;
	    CustomForces: boolean;
	    CustomTechtree: boolean;
	    CustomAbilities: boolean;
	    CustomUpgrades: boolean;
	    Unknown2: boolean;
	    CliffShoreWaves: boolean;
	    RollingShoreWaves: boolean;
	    Unknown3: boolean;
	    Unknown4: boolean;
	    ItemClassification: boolean;
	    WaterTinting: boolean;
	    AccurateProbabilityForCalculations: boolean;
	    CustomAbilitySkins: boolean;
	    DisableDenyIcon: boolean;
	    ForceDefaultZoom: boolean;
	    ForceMaxZoom: boolean;
	    ForceMinZoom: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Flags(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Raw = source["Raw"];
	        this.HideMinimapPreview = source["HideMinimapPreview"];
	        this.ModifyAllyPriorities = source["ModifyAllyPriorities"];
	        this.MeleeMap = source["MeleeMap"];
	        this.Unknown1 = source["Unknown1"];
	        this.MaskedAreaPartiallyVisible = source["MaskedAreaPartiallyVisible"];
	        this.FixedPlayerSettings = source["FixedPlayerSettings"];
	        this.CustomForces = source["CustomForces"];
	        this.CustomTechtree = source["CustomTechtree"];
	        this.CustomAbilities = source["CustomAbilities"];
	        this.CustomUpgrades = source["CustomUpgrades"];
	        this.Unknown2 = source["Unknown2"];
	        this.CliffShoreWaves = source["CliffShoreWaves"];
	        this.RollingShoreWaves = source["RollingShoreWaves"];
	        this.Unknown3 = source["Unknown3"];
	        this.Unknown4 = source["Unknown4"];
	        this.ItemClassification = source["ItemClassification"];
	        this.WaterTinting = source["WaterTinting"];
	        this.AccurateProbabilityForCalculations = source["AccurateProbabilityForCalculations"];
	        this.CustomAbilitySkins = source["CustomAbilitySkins"];
	        this.DisableDenyIcon = source["DisableDenyIcon"];
	        this.ForceDefaultZoom = source["ForceDefaultZoom"];
	        this.ForceMaxZoom = source["ForceMaxZoom"];
	        this.ForceMinZoom = source["ForceMinZoom"];
	    }
	}
	export class Fog {
	    Style: number;
	    StartZ: number;
	    EndZ: number;
	    Density: number;
	    Color: Color;
	
	    static createFrom(source: any = {}) {
	        return new Fog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Style = source["Style"];
	        this.StartZ = source["StartZ"];
	        this.EndZ = source["EndZ"];
	        this.Density = source["Density"];
	        this.Color = this.convertValues(source["Color"], Color);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ForceFlags {
	    Raw: number;
	    Allied: boolean;
	    AlliedVictory: boolean;
	    ShareVision: boolean;
	    ShareUnitControl: boolean;
	    ShareAdvancedUnitControl: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ForceFlags(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Raw = source["Raw"];
	        this.Allied = source["Allied"];
	        this.AlliedVictory = source["AlliedVictory"];
	        this.ShareVision = source["ShareVision"];
	        this.ShareUnitControl = source["ShareUnitControl"];
	        this.ShareAdvancedUnitControl = source["ShareAdvancedUnitControl"];
	    }
	}
	export class Force {
	    Flags: ForceFlags;
	    PlayerMasks: number;
	    Name: string;
	
	    static createFrom(source: any = {}) {
	        return new Force(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Flags = this.convertValues(source["Flags"], ForceFlags);
	        this.PlayerMasks = source["PlayerMasks"];
	        this.Name = source["Name"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class IVec4 {
	    A: number;
	    B: number;
	    C: number;
	    D: number;
	
	    static createFrom(source: any = {}) {
	        return new IVec4(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.A = source["A"];
	        this.B = source["B"];
	        this.C = source["C"];
	        this.D = source["D"];
	    }
	}
	export class RandomItemEntry {
	    Chance: number;
	    ID: string;
	
	    static createFrom(source: any = {}) {
	        return new RandomItemEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Chance = source["Chance"];
	        this.ID = source["ID"];
	    }
	}
	export class RandomItemSet {
	    Items: RandomItemEntry[];
	
	    static createFrom(source: any = {}) {
	        return new RandomItemSet(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Items = this.convertValues(source["Items"], RandomItemEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RandomItemTable {
	    CreationNumber: number;
	    Name: string;
	    ItemSets: RandomItemSet[];
	
	    static createFrom(source: any = {}) {
	        return new RandomItemTable(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.CreationNumber = source["CreationNumber"];
	        this.Name = source["Name"];
	        this.ItemSets = this.convertValues(source["ItemSets"], RandomItemSet);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RandomUnitLine {
	    Chance: number;
	    IDs: string[];
	
	    static createFrom(source: any = {}) {
	        return new RandomUnitLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Chance = source["Chance"];
	        this.IDs = source["IDs"];
	    }
	}
	export class RandomUnitTable {
	    CreationNumber: number;
	    Name: string;
	    Positions: number[];
	    Lines: RandomUnitLine[];
	
	    static createFrom(source: any = {}) {
	        return new RandomUnitTable(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.CreationNumber = source["CreationNumber"];
	        this.Name = source["Name"];
	        this.Positions = source["Positions"];
	        this.Lines = this.convertValues(source["Lines"], RandomUnitLine);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TechAvailability {
	    PlayerFlags: number;
	    ID: string;
	
	    static createFrom(source: any = {}) {
	        return new TechAvailability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.PlayerFlags = source["PlayerFlags"];
	        this.ID = source["ID"];
	    }
	}
	export class UpgradeAvailability {
	    PlayerFlags: number;
	    ID: string;
	    Level: number;
	    Availability: number;
	
	    static createFrom(source: any = {}) {
	        return new UpgradeAvailability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.PlayerFlags = source["PlayerFlags"];
	        this.ID = source["ID"];
	        this.Level = source["Level"];
	        this.Availability = source["Availability"];
	    }
	}
	export class Player {
	    InternalNumber: number;
	    Type: number;
	    Race: number;
	    FixedStartPosition: number;
	    Name: string;
	    StartingPosition: Vec2;
	    AllyLowPriorities: number;
	    AllyHighPriorities: number;
	    EnemyLowPriorities: number;
	    EnemyHighPriorities: number;
	
	    static createFrom(source: any = {}) {
	        return new Player(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.InternalNumber = source["InternalNumber"];
	        this.Type = source["Type"];
	        this.Race = source["Race"];
	        this.FixedStartPosition = source["FixedStartPosition"];
	        this.Name = source["Name"];
	        this.StartingPosition = this.convertValues(source["StartingPosition"], Vec2);
	        this.AllyLowPriorities = source["AllyLowPriorities"];
	        this.AllyHighPriorities = source["AllyHighPriorities"];
	        this.EnemyLowPriorities = source["EnemyLowPriorities"];
	        this.EnemyHighPriorities = source["EnemyHighPriorities"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Prologue {
	    Model: string;
	    Text: string;
	    Title: string;
	    Subtitle: string;
	
	    static createFrom(source: any = {}) {
	        return new Prologue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Model = source["Model"];
	        this.Text = source["Text"];
	        this.Title = source["Title"];
	        this.Subtitle = source["Subtitle"];
	    }
	}
	export class LoadingScreen {
	    Number: number;
	    Model: string;
	    Text: string;
	    Title: string;
	    Subtitle: string;
	
	    static createFrom(source: any = {}) {
	        return new LoadingScreen(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Number = source["Number"];
	        this.Model = source["Model"];
	        this.Text = source["Text"];
	        this.Title = source["Title"];
	        this.Subtitle = source["Subtitle"];
	    }
	}
	export class Vec2 {
	    X: number;
	    Y: number;
	
	    static createFrom(source: any = {}) {
	        return new Vec2(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.X = source["X"];
	        this.Y = source["Y"];
	    }
	}
	export class Info {
	    FileVersion: number;
	    MapVersion: number;
	    EditorVersion: number;
	    GameVersion: number[];
	    Name: string;
	    Author: string;
	    Description: string;
	    SuggestedPlayers: string;
	    CameraLeftBottom: Vec2;
	    CameraRightTop: Vec2;
	    CameraLeftTop: Vec2;
	    CameraRightBottom: Vec2;
	    CameraComplements: IVec4;
	    PlayableWidth: number;
	    PlayableHeight: number;
	    Flags: Flags;
	    Tileset: number;
	    LoadingScreen: LoadingScreen;
	    GameDataSet: number;
	    Prologue: Prologue;
	    Fog: Fog;
	    WeatherID: number;
	    CustomSoundEnv: string;
	    CustomLightTileset: number;
	    WaterColor: Color;
	    Lua: boolean;
	    SupportedModes: number;
	    GameDataVersion: number;
	    CamDistance: CamDistance;
	    Players: Player[];
	    Forces: Force[];
	    AvailableUpgrades: UpgradeAvailability[];
	    AvailableTech: TechAvailability[];
	    RandomUnitTables: RandomUnitTable[];
	    RandomItemTables: RandomItemTable[];
	
	    static createFrom(source: any = {}) {
	        return new Info(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.FileVersion = source["FileVersion"];
	        this.MapVersion = source["MapVersion"];
	        this.EditorVersion = source["EditorVersion"];
	        this.GameVersion = source["GameVersion"];
	        this.Name = source["Name"];
	        this.Author = source["Author"];
	        this.Description = source["Description"];
	        this.SuggestedPlayers = source["SuggestedPlayers"];
	        this.CameraLeftBottom = this.convertValues(source["CameraLeftBottom"], Vec2);
	        this.CameraRightTop = this.convertValues(source["CameraRightTop"], Vec2);
	        this.CameraLeftTop = this.convertValues(source["CameraLeftTop"], Vec2);
	        this.CameraRightBottom = this.convertValues(source["CameraRightBottom"], Vec2);
	        this.CameraComplements = this.convertValues(source["CameraComplements"], IVec4);
	        this.PlayableWidth = source["PlayableWidth"];
	        this.PlayableHeight = source["PlayableHeight"];
	        this.Flags = this.convertValues(source["Flags"], Flags);
	        this.Tileset = source["Tileset"];
	        this.LoadingScreen = this.convertValues(source["LoadingScreen"], LoadingScreen);
	        this.GameDataSet = source["GameDataSet"];
	        this.Prologue = this.convertValues(source["Prologue"], Prologue);
	        this.Fog = this.convertValues(source["Fog"], Fog);
	        this.WeatherID = source["WeatherID"];
	        this.CustomSoundEnv = source["CustomSoundEnv"];
	        this.CustomLightTileset = source["CustomLightTileset"];
	        this.WaterColor = this.convertValues(source["WaterColor"], Color);
	        this.Lua = source["Lua"];
	        this.SupportedModes = source["SupportedModes"];
	        this.GameDataVersion = source["GameDataVersion"];
	        this.CamDistance = this.convertValues(source["CamDistance"], CamDistance);
	        this.Players = this.convertValues(source["Players"], Player);
	        this.Forces = this.convertValues(source["Forces"], Force);
	        this.AvailableUpgrades = this.convertValues(source["AvailableUpgrades"], UpgradeAvailability);
	        this.AvailableTech = this.convertValues(source["AvailableTech"], TechAvailability);
	        this.RandomUnitTables = this.convertValues(source["RandomUnitTables"], RandomUnitTable);
	        this.RandomItemTables = this.convertValues(source["RandomItemTables"], RandomItemTable);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	
	
	
	
	

}


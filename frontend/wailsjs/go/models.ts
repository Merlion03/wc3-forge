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

export namespace main {
	
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
	
	export class TerrainDTO {
	    width: number;
	    height: number;
	    center_offset: number[];
	    heights: number[];
	    ground_tex: number[];
	    tileset: string;
	    palette: string[];
	    palette_colors: number[][];
	    layer_height: number[];
	    cliff_tex: number[];
	    cliff_var: number[];
	    ramp_flags: number[];
	    cliff_palette: string[];
	
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
	        this.layer_height = source["layer_height"];
	        this.cliff_tex = source["cliff_tex"];
	        this.cliff_var = source["cliff_var"];
	        this.ramp_flags = source["ramp_flags"];
	        this.cliff_palette = source["cliff_palette"];
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


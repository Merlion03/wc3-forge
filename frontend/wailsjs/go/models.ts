export namespace main {
	
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


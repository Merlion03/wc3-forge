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


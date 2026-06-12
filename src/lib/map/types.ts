export interface Heading {
  text: string;
  level: number;
  line: number;
}

export interface Section {
  heading: string;
  level: number;
  line_range: [number, number];
  wikilinks: string[];
  children: Section[];
}

export interface Wikilink {
  target: string;
  heading?: string;
  blockRef?: string;
  display?: string;
  line: number;
}

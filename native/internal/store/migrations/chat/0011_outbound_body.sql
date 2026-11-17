-- 历史只升级数据形状，待发计划仍由 Outlet.Sweep 中止，不恢复发送。
-- +goose Up
UPDATE outbound_parts SET text=json_array(json_object('kind','text','text',COALESCE(text,''))), kind='message' WHERE kind='text';
UPDATE outbound_parts SET text=json_array(json_object('kind',CASE kind WHEN 'emoji' THEN 'gif' ELSE 'image' END,'media',json_object('id',COALESCE(ref,''),'mime','','size',0))),ref=NULL,kind='message' WHERE kind IN ('image','emoji');
UPDATE outbound_parts SET text=(SELECT json_group_array(json(json_set(json_remove(value,'$.text'),'$.body',json_array(json_object('kind','text','text',json_extract(value,'$.text')))))) FROM json_each(outbound_parts.text)) WHERE kind='forward' AND json_valid(text) AND EXISTS(SELECT 1 FROM json_each(outbound_parts.text) WHERE json_type(value,'$.text')='text');
UPDATE events SET segments_json=json_set(segments_json,'$.parts',json((SELECT json_group_array(json(CASE json_extract(p.value,'$.kind')
 WHEN 'text' THEN json_set(json_remove(p.value,'$.text'),'$.kind','message','$.body',json_array(json_object('kind','text','text',json_extract(p.value,'$.text'))))
 WHEN 'image' THEN json_set(json_remove(p.value,'$.ref'),'$.kind','message','$.body',json_array(json_object('kind','image','media',json_object('id',COALESCE(json_extract(p.value,'$.ref'),''),'mime','','size',0))))
 WHEN 'emoji' THEN json_set(json_remove(p.value,'$.ref'),'$.kind','message','$.body',json_array(json_object('kind','gif','media',json_object('id',COALESCE(json_extract(p.value,'$.ref'),''),'mime','','size',0))))
 WHEN 'forward' THEN json_set(p.value,'$.nodes',json((SELECT json_group_array(json(CASE WHEN json_type(n.value,'$.text')='text' THEN json_set(json_remove(n.value,'$.text'),'$.body',json_array(json_object('kind','text','text',json_extract(n.value,'$.text')))) ELSE n.value END)) FROM json_each(json_extract(p.value,'$.nodes')) n)))
 ELSE p.value END)) FROM json_each(json_extract(events.segments_json,'$.parts')) p))) WHERE json_valid(segments_json) AND json_type(segments_json,'$.parts')='array';

-- +goose Down
-- 已完成历史不重新生成旧发送通路。
SELECT 1;
